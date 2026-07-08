package attach

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// roundTripperFunc stubs an http.RoundTripper so the converter calls can be
// exercised without binding a TCP port (the sandbox forbids it).
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// stubConverter points converterClient at fn for the duration of a test and
// restores the original transport afterward.
func stubConverter(t *testing.T, fn roundTripperFunc) {
	t.Helper()
	orig := converterClient.Transport
	converterClient.Transport = fn
	t.Cleanup(func() { converterClient.Transport = orig })
}

func okResp(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
	}
}

func TestIsIWork(t *testing.T) {
	for _, p := range []string{"a.pages", "b.NUMBERS", "c.key", "/x/y/deck.Key"} {
		if !IsIWork(p) {
			t.Errorf("IsIWork(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"a.docx", "b.pdf", "c.png", "d.txt"} {
		if IsIWork(p) {
			t.Errorf("IsIWork(%q) = true, want false", p)
		}
	}
}

func TestConvertIWorkUploadsAndReturnsBytes(t *testing.T) {
	want := []byte("converted-office-bytes")
	var gotMethod, gotPath, gotField, gotFilename string
	stubConverter(t, func(r *http.Request) (*http.Response, error) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err == nil && r.MultipartForm != nil {
			for field, files := range r.MultipartForm.File {
				gotField = field
				if len(files) > 0 {
					gotFilename = files[0].Filename
				}
			}
		}
		return okResp(want), nil
	})

	got, err := convertIWork("report.pages", []byte("fake pages bundle"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("convertIWork returned %q, want %q", got, want)
	}
	if gotMethod != http.MethodPost || gotPath != "/convert/to-office" {
		t.Errorf("request was %s %s, want POST /convert/to-office", gotMethod, gotPath)
	}
	if gotField != "file" || gotFilename != "report.pages" {
		t.Errorf("multipart field=%q filename=%q, want file/report.pages", gotField, gotFilename)
	}
}

func TestFromBytesIWorkConvertsThenExtracts(t *testing.T) {
	// The converter returns a .docx; FromBytes should text-extract it and keep
	// the original .pages display name.
	docx := makeDocx(t, "Quarterly summary", "Revenue is up")
	stubConverter(t, func(*http.Request) (*http.Response, error) { return okResp(docx), nil })

	a, err := FromBytes("report.pages", "", []byte("fake pages bundle"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Kind != KindText {
		t.Fatalf("want text kind, got %v", a.Kind)
	}
	if a.Name != "report.pages" {
		t.Errorf("display name = %q, want report.pages", a.Name)
	}
	if !strings.Contains(a.Text, "Quarterly summary") || !strings.Contains(a.Text, "Revenue is up") {
		t.Fatalf("extracted text missing converted content: %q", a.Text)
	}
}

func TestFromBytesIWorkConverterError(t *testing.T) {
	stubConverter(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       io.NopCloser(strings.NewReader(`{"error":"AppleScript failed"}`)),
		}, nil
	})
	_, err := FromBytes("deck.key", "", []byte("fake keynote bundle"))
	if err == nil {
		t.Fatal("expected an error when the converter returns 500")
	}
	if !strings.Contains(err.Error(), "AppleScript failed") {
		t.Errorf("error should surface the converter's reason, got: %v", err)
	}
}

func TestFromBytesIWorkDisabled(t *testing.T) {
	t.Setenv("MOONY_CONVERTER_URL", "") // explicitly empty → conversion disabled
	_, err := FromBytes("notes.numbers", "", []byte("fake numbers bundle"))
	if err == nil {
		t.Fatal("expected an error when conversion is disabled")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error should explain conversion is disabled, got: %v", err)
	}
}
