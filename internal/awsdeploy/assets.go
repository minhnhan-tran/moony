// Package awsdeploy deploys the moony relay to a single EC2 instance: Caddy
// terminates TLS in front of the browser port and the raw connector tunnel is
// published on an Elastic IP, so no load balancers are involved.
//
// The CloudFormation template and the files the instance runs are embedded, so they
// travel with the binary and cannot drift from the code that drives them. AWS itself is
// reached by shelling out to the `aws` CLI rather than linking the SDK: that keeps
// profiles, SSO and `aws login` working exactly as they do in a terminal, and keeps this
// package free of a credential-resolution implementation of its own.
package awsdeploy

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed assets/cloudformation.yaml assets/compose.aws.yml assets/Caddyfile assets/apply.sh
var assets embed.FS

// AssetDir is where the embedded files live in a checkout, and therefore where they
// land in the source tarball the instance unpacks. The instance runs apply.sh from
// there, so this constant and the paths inside the assets have to agree.
const AssetDir = "internal/awsdeploy/assets"

// Template returns the CloudFormation template.
func Template() []byte { return mustAsset("cloudformation.yaml") }

// Asset returns one embedded deployment file by base name.
func Asset(name string) ([]byte, error) {
	b, err := assets.ReadFile("assets/" + name)
	if err != nil {
		return nil, fmt.Errorf("embedded asset %q: %w", name, err)
	}
	return b, nil
}

// AssetNames lists the embedded deployment files.
func AssetNames() []string {
	return []string{"cloudformation.yaml", "compose.aws.yml", "Caddyfile", "apply.sh"}
}

func mustAsset(name string) []byte {
	b, err := Asset(name)
	if err != nil {
		// Unreachable: the embed directive above names these files, so a failure here
		// means the binary was built from a tree that cannot exist.
		panic(err)
	}
	return b
}

// applyPath is the instance-side path of apply.sh, under the unpacked tarball.
func applyPath() string {
	return strings.TrimSuffix(instanceSrcDir, "/") + "/" + AssetDir + "/apply.sh"
}
