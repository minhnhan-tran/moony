package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// mobileTool drives iOS simulators (via `xcrun simctl`, optionally `idb` for
// input injection) and Android emulators/devices (via `adb`) for build→deploy→
// interact→verify workflows: booting a sim/device, installing and launching a
// built app, tapping/swiping/typing, pulling logs, and screenshotting the
// screen so the model can literally see the result (ExecuteImages, mirroring
// the browser tool). Actual app compilation (xcodebuild / gradlew) stays on
// the bash tool — this tool only covers what bash can't: real device/simulator
// control and pixels back to the model.
type mobileTool struct {
	r *Registry
}

func (t *mobileTool) Name() string { return "mobile" }

func (t *mobileTool) Description() string {
	return "Control an iOS simulator/device or Android emulator/device: list devices, " +
		"boot a simulator/emulator, install and launch a built app (.app/.apk/.ipa/AAB-installed-APK), " +
		"tap/swipe/type/press keys, read logs, and take a screenshot (returned as an image so you can " +
		"see the screen). Build the app first with the bash tool (xcodebuild/xcrun for iOS, " +
		"./gradlew for Android); this tool only handles device control, not compilation. " +
		"iOS input injection (tap/swipe/text/key) requires `idb` (Meta's iOS debug bridge) on PATH; " +
		"without it, use screenshots to verify and note the missing capability."
}

func (t *mobileTool) Mutates() bool { return true } // side-effecting: installs apps, changes device state

func (t *mobileTool) Schema() json.RawMessage {
	return schema(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": [
				"devices", "boot", "shutdown", "install", "uninstall", "launch", "stop",
				"tap", "swipe", "text", "key", "screenshot", "logs"
			], "description": "What to do."},
			"platform": {"type": "string", "enum": ["ios", "android"], "description": "Required for every action except 'devices' (which lists both)."},
			"device_id": {"type": "string", "description": "iOS simulator UDID (from 'devices'), or Android device/emulator serial (from 'adb devices'). Optional for Android when exactly one device is attached."},
			"path": {"type": "string", "description": "install: path to the .app bundle (iOS simulator) or .ipa (real device, needs ios-deploy) or .apk (Android)."},
			"bundle_id": {"type": "string", "description": "launch/stop/uninstall: iOS bundle identifier (e.g. com.example.App) or Android package name (e.g. com.example.app)."},
			"activity": {"type": "string", "description": "launch (Android only): Activity to start, e.g. .MainActivity. Defaults to the package's default launcher activity."},
			"x": {"type": "number", "description": "tap/swipe: start X in device points/pixels."},
			"y": {"type": "number", "description": "tap/swipe: start Y."},
			"x2": {"type": "number", "description": "swipe: end X."},
			"y2": {"type": "number", "description": "swipe: end Y."},
			"text": {"type": "string", "description": "text action: the string to type into the focused field."},
			"key": {"type": "string", "description": "key action: a key name. Android: keyevent name/code (e.g. ENTER, BACK, KEYCODE_HOME). iOS (via idb): a key name idb understands (e.g. RETURN)."},
			"lines": {"type": "number", "description": "logs: how many recent lines to return (default 200)."},
			"timeout_sec": {"type": "number", "description": "Max seconds to wait for boot/install/launch (default 60, max 300)."}
		},
		"required": ["action"]
	}`)
}

type mobileInput struct {
	Action     string  `json:"action"`
	Platform   string  `json:"platform"`
	DeviceID   string  `json:"device_id"`
	Path       string  `json:"path"`
	BundleID   string  `json:"bundle_id"`
	Activity   string  `json:"activity"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	X2         float64 `json:"x2"`
	Y2         float64 `json:"y2"`
	Text       string  `json:"text"`
	Key        string  `json:"key"`
	Lines      int     `json:"lines"`
	TimeoutSec float64 `json:"timeout_sec"`
}

// Execute satisfies the plain Tool interface (no image). Screenshot goes
// through ExecuteImages; everything else is delegated here too so callers that
// only support Tool still work (they just never see the screenshot's pixels).
func (t *mobileTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	out, _, err := t.ExecuteImages(ctx, input)
	return out, err
}

func (t *mobileTool) ExecuteImages(ctx context.Context, input json.RawMessage) (string, []Image, error) {
	var in mobileInput
	if err := decode(input, &in); err != nil {
		return "", nil, err
	}
	if in.Action == "" {
		return "", nil, errors.New("action is required")
	}
	timeout := 60 * time.Second
	if in.TimeoutSec > 0 {
		timeout = time.Duration(in.TimeoutSec) * time.Second
	}
	if timeout > 300*time.Second {
		timeout = 300 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if in.Action == "devices" {
		return t.listDevices(cctx)
	}

	switch in.Platform {
	case "ios":
		if in.Action == "screenshot" {
			return t.iosScreenshot(cctx, in)
		}
		out, err := t.ios(cctx, in)
		return out, nil, err
	case "android":
		return t.android(cctx, in)
	case "":
		return "", nil, fmt.Errorf("platform is required for action %q (ios or android)", in.Action)
	default:
		return "", nil, fmt.Errorf("unknown platform %q (want ios or android)", in.Platform)
	}
}

// --- device listing -------------------------------------------------------

func (t *mobileTool) listDevices(ctx context.Context) (string, []Image, error) {
	var b strings.Builder
	b.WriteString("iOS simulators (xcrun simctl):\n")
	if out, err := runCapture(ctx, "xcrun", "simctl", "list", "devices", "available"); err != nil {
		b.WriteString("  (xcrun/simctl unavailable: " + firstLine(err.Error()) + ")\n")
	} else {
		b.WriteString(indent(out))
	}
	b.WriteString("\nAndroid devices/emulators (adb devices -l):\n")
	if out, err := runCapture(ctx, "adb", "devices", "-l"); err != nil {
		b.WriteString("  (adb unavailable: " + firstLine(err.Error()) + ")\n")
	} else {
		b.WriteString(indent(out))
	}
	b.WriteString("\nAndroid AVDs (emulator -list-avds):\n")
	if out, err := runCapture(ctx, "emulator", "-list-avds"); err != nil {
		b.WriteString("  (emulator unavailable: " + firstLine(err.Error()) + ")\n")
	} else {
		b.WriteString(indent(out))
	}
	return b.String(), nil, nil
}

// --- iOS (xcrun simctl, idb for input) ------------------------------------

func (t *mobileTool) ios(ctx context.Context, in mobileInput) (string, error) {
	switch in.Action {
	case "boot":
		if in.DeviceID == "" {
			return "", errors.New("boot requires device_id (a simulator UDID from 'devices')")
		}
		out, err := runCapture(ctx, "xcrun", "simctl", "boot", in.DeviceID)
		if err != nil && !strings.Contains(err.Error(), "current state: Booted") {
			return out, err
		}
		if _, err := runCapture(ctx, "open", "-a", "Simulator"); err != nil {
			return out + "\n(booted; failed to bring the Simulator app to the foreground: " + firstLine(err.Error()) + ")", nil
		}
		return "Booted " + in.DeviceID, nil

	case "shutdown":
		if in.DeviceID == "" {
			return "", errors.New("shutdown requires device_id")
		}
		return runCapture(ctx, "xcrun", "simctl", "shutdown", in.DeviceID)

	case "install":
		if in.DeviceID == "" || in.Path == "" {
			return "", errors.New("install requires device_id and path (.app bundle for a simulator)")
		}
		if strings.HasSuffix(in.Path, ".ipa") {
			// Real device install needs a separate tool; simctl only takes .app bundles.
			if _, err := exec.LookPath("ios-deploy"); err != nil {
				return "", errors.New("installing a .ipa on a real iOS device requires `ios-deploy` (brew install ios-deploy); not found on PATH")
			}
			return runCapture(ctx, "ios-deploy", "--id", in.DeviceID, "--bundle", in.Path)
		}
		return runCapture(ctx, "xcrun", "simctl", "install", in.DeviceID, in.Path)

	case "uninstall":
		if in.DeviceID == "" || in.BundleID == "" {
			return "", errors.New("uninstall requires device_id and bundle_id")
		}
		return runCapture(ctx, "xcrun", "simctl", "uninstall", in.DeviceID, in.BundleID)

	case "launch":
		if in.DeviceID == "" || in.BundleID == "" {
			return "", errors.New("launch requires device_id and bundle_id")
		}
		return runCapture(ctx, "xcrun", "simctl", "launch", in.DeviceID, in.BundleID)

	case "stop":
		if in.DeviceID == "" || in.BundleID == "" {
			return "", errors.New("stop requires device_id and bundle_id")
		}
		return runCapture(ctx, "xcrun", "simctl", "terminate", in.DeviceID, in.BundleID)

	case "logs":
		if in.DeviceID == "" {
			return "", errors.New("logs requires device_id")
		}
		return runCapture(ctx, "xcrun", "simctl", "spawn", in.DeviceID, "log", "show", "--last", "2m", "--style", "compact")

	case "tap", "swipe", "text", "key":
		return t.iosInput(ctx, in)

	default:
		return "", fmt.Errorf("unsupported ios action %q", in.Action)
	}
}

// iosInput injects taps/swipes/text/keys via idb (simctl has no input API of
// its own). Returns a clear, actionable error when idb isn't installed rather
// than silently no-op'ing.
func (t *mobileTool) iosInput(ctx context.Context, in mobileInput) (string, error) {
	if _, err := exec.LookPath("idb"); err != nil {
		return "", errors.New("tap/swipe/text/key on iOS requires `idb` (Meta's iOS debug bridge: brew tap facebook/fb && brew install idb-companion, pip3 install fb-idb); simctl alone can't inject input. Use screenshot to verify state in the meantime")
	}
	if in.DeviceID == "" {
		return "", fmt.Errorf("%s requires device_id", in.Action)
	}
	switch in.Action {
	case "tap":
		return runCapture(ctx, "idb", "ui", "tap", "--udid", in.DeviceID, fmt2(in.X), fmt2(in.Y))
	case "swipe":
		return runCapture(ctx, "idb", "ui", "swipe", "--udid", in.DeviceID, fmt2(in.X), fmt2(in.Y), fmt2(in.X2), fmt2(in.Y2))
	case "text":
		if in.Text == "" {
			return "", errors.New("text requires text")
		}
		return runCapture(ctx, "idb", "ui", "text", "--udid", in.DeviceID, in.Text)
	case "key":
		if in.Key == "" {
			return "", errors.New("key requires key")
		}
		return runCapture(ctx, "idb", "ui", "key", "--udid", in.DeviceID, in.Key)
	}
	return "", fmt.Errorf("unsupported ios input action %q", in.Action)
}

// --- Android (adb, emulator) ----------------------------------------------

func (t *mobileTool) android(ctx context.Context, in mobileInput) (string, []Image, error) {
	// screenshot is the only action that returns an image; everything else
	// returns plain text.
	if in.Action == "screenshot" {
		out, img, err := t.androidScreenshot(ctx, in)
		return out, img, err
	}
	out, err := t.androidText(ctx, in)
	return out, nil, err
}

func adbArgs(deviceID string, args ...string) []string {
	if deviceID == "" {
		return args
	}
	return append([]string{"-s", deviceID}, args...)
}

func (t *mobileTool) androidText(ctx context.Context, in mobileInput) (string, error) {
	switch in.Action {
	case "boot":
		if in.DeviceID == "" {
			return "", errors.New("boot requires device_id (an AVD name from 'devices')")
		}
		// emulator blocks until it exits, so run it detached and just report launch.
		cmd := exec.Command("emulator", "-avd", in.DeviceID, "-no-snapshot-save")
		if err := cmd.Start(); err != nil {
			return "", fmt.Errorf("failed to start emulator: %w", err)
		}
		return fmt.Sprintf("Launched emulator -avd %s (pid %d); poll 'devices' until it shows as online.", in.DeviceID, cmd.Process.Pid), nil

	case "shutdown":
		return runCapture(ctx, "adb", adbArgs(in.DeviceID, "emu", "kill")...)

	case "install":
		if in.Path == "" {
			return "", errors.New("install requires path (.apk)")
		}
		return runCapture(ctx, "adb", adbArgs(in.DeviceID, "install", "-r", in.Path)...)

	case "uninstall":
		if in.BundleID == "" {
			return "", errors.New("uninstall requires bundle_id (Android package name)")
		}
		return runCapture(ctx, "adb", adbArgs(in.DeviceID, "uninstall", in.BundleID)...)

	case "launch":
		if in.BundleID == "" {
			return "", errors.New("launch requires bundle_id (Android package name)")
		}
		if in.Activity == "" {
			// Let the package manager resolve the launcher activity itself
			// instead of guessing at .MainActivity.
			return runCapture(ctx, "adb", adbArgs(in.DeviceID, "shell", "monkey", "-p", in.BundleID, "-c", "android.intent.category.LAUNCHER", "1")...)
		}
		// A leading "." is a relative activity name under the package; a name
		// with no dot is treated the same way; a fully-qualified name is used as-is.
		activity := in.Activity
		if !strings.Contains(activity, ".") {
			activity = "." + activity
		}
		target := in.BundleID + "/" + activity
		return runCapture(ctx, "adb", adbArgs(in.DeviceID, "shell", "am", "start", "-n", target)...)

	case "stop":
		if in.BundleID == "" {
			return "", errors.New("stop requires bundle_id")
		}
		return runCapture(ctx, "adb", adbArgs(in.DeviceID, "shell", "am", "force-stop", in.BundleID)...)

	case "tap":
		return runCapture(ctx, "adb", adbArgs(in.DeviceID, "shell", "input", "tap", fmt2(in.X), fmt2(in.Y))...)

	case "swipe":
		return runCapture(ctx, "adb", adbArgs(in.DeviceID, "shell", "input", "swipe", fmt2(in.X), fmt2(in.Y), fmt2(in.X2), fmt2(in.Y2))...)

	case "text":
		if in.Text == "" {
			return "", errors.New("text requires text")
		}
		return runCapture(ctx, "adb", adbArgs(in.DeviceID, "shell", "input", "text", androidEscapeText(in.Text))...)

	case "key":
		if in.Key == "" {
			return "", errors.New("key requires key")
		}
		return runCapture(ctx, "adb", adbArgs(in.DeviceID, "shell", "input", "keyevent", in.Key)...)

	case "logs":
		n := in.Lines
		if n <= 0 {
			n = 200
		}
		out, err := runCapture(ctx, "adb", adbArgs(in.DeviceID, "logcat", "-d", "-t", strconv.Itoa(n))...)
		return out, err

	default:
		return "", fmt.Errorf("unsupported android action %q", in.Action)
	}
}

func (t *mobileTool) androidScreenshot(ctx context.Context, in mobileInput) (string, []Image, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "adb", adbArgs(in.DeviceID, "exec-out", "screencap", "-p")...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureProcessGroup(cmd)
	if err := cmd.Run(); err != nil {
		return "", nil, fmt.Errorf("adb screencap failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return "", nil, errors.New("adb screencap returned no data (no device attached?)")
	}
	return fmt.Sprintf("Captured Android screenshot (%d bytes).", stdout.Len()),
		[]Image{{MediaType: "image/png", Data: stdout.Bytes()}}, nil
}

// iosScreenshot is invoked from ExecuteImages for platform=ios, action=screenshot.
func (t *mobileTool) iosScreenshot(ctx context.Context, in mobileInput) (string, []Image, error) {
	if in.DeviceID == "" {
		return "", nil, errors.New("screenshot requires device_id")
	}
	tmp, err := os.CreateTemp("", "sqe-ios-shot-*.png")
	if err != nil {
		return "", nil, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)

	if _, err := runCapture(ctx, "xcrun", "simctl", "io", in.DeviceID, "screenshot", path); err != nil {
		return "", nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fmt.Errorf("screenshot written but unreadable: %w", err)
	}
	return fmt.Sprintf("Captured iOS simulator screenshot (%d bytes).", len(data)),
		[]Image{{MediaType: "image/png", Data: data}}, nil
}

// --- helpers ---------------------------------------------------------------

func runCapture(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	configureProcessGroup(cmd)
	out, err := cmd.CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		if trimmed != "" {
			return trimmed, fmt.Errorf("%s: %s", err, trimmed)
		}
		return trimmed, err
	}
	return trimmed, nil
}

func fmt2(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func indent(s string) string {
	if s == "" {
		return "  (none)\n"
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

// androidEscapeText escapes characters `adb shell input text` treats
// specially (spaces and shell metacharacters), since the string travels
// through an extra shell hop on the device.
func androidEscapeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ':
			b.WriteString("%s")
		case '&', '|', ';', '<', '>', '(', ')', '$', '`', '"', '\'', '\\', '*', '~':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
