package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/provider"
)

// AWS Bedrock backend. Bedrock has no OpenAI-compatible endpoint: requests are
// SigV4-signed and the response is streamed over the AWS event-stream binary
// protocol. We use the AWS SDK (bedrockruntime) to handle signing and
// event-stream decoding, and reuse the Anthropic wire format for the request
// body and the shared SSE parser (parseStream) for the decoded events — a
// Bedrock InvokeModelWithResponseStream chunk carries exactly one Anthropic
// streaming event as raw JSON, so we re-frame each chunk as an SSE "data:" line
// and feed it to parseStream unchanged.
//
// The invoked model reuses moony's thinking/effort logic via buildAnthropicBody;
// only the Bedrock-specific body shape differs (no top-level "model"/"stream",
// plus the required "anthropic_version" field).

// bedrockAnthropicVersion is the fixed version string Bedrock requires in the
// Claude Messages request body.
const bedrockAnthropicVersion = "bedrock-2023-05-31"

// usesBedrock reports whether the configured provider is AWS Bedrock.
func (c *Client) usesBedrock() bool {
	return c.cfg != nil && provider.UsesBedrockRuntime(c.cfg.LLMProvider)
}

// listBedrockModels returns the configured Bedrock model. Enumerating the
// account's catalog needs the separate Bedrock control-plane API; rather than
// pull in another client, we surface the current default and let users set any
// model / inference-profile id with /model.
func (c *Client) listBedrockModels(ctx context.Context) ([]config.ModelInfo, error) {
	if _, err := c.bedrockCredentials(ctx); err != nil {
		return nil, err
	}
	m := c.bedrockModelID()
	if m == "" {
		return nil, nil
	}
	return []config.ModelInfo{{ID: m, Name: m}}, nil
}

// bedrockModelID resolves the Bedrock model / inference-profile id to invoke.
func (c *Client) bedrockModelID() string {
	if c.cfg != nil {
		if m := strings.TrimSpace(c.cfg.Model); m != "" {
			return m
		}
	}
	return provider.DefaultModelFor(provider.Bedrock)
}

// bedrockAWSConfig resolves the AWS config (region + credentials) for Bedrock.
// Explicit MOONY_AWS_*/AWS_* keys win; otherwise the SDK's default chain
// (shared config, SSO, env, instance role) is used, optionally scoped to a
// named profile.
func (c *Client) bedrockAWSConfig(ctx context.Context) (aws.Config, error) {
	if c.cfg == nil {
		return aws.Config{}, errors.New("bedrock: no configuration")
	}
	region := strings.TrimSpace(c.cfg.AWSRegion)
	if region == "" {
		region = "us-east-1"
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(region)}

	key := strings.TrimSpace(c.cfg.AWSAccessKeyID)
	secret := strings.TrimSpace(c.cfg.AWSSecretAccessKey)
	switch {
	case key != "" && secret != "":
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(key, secret, strings.TrimSpace(c.cfg.AWSSessionToken)),
		))
	case strings.TrimSpace(c.cfg.AWSProfile) != "":
		opts = append(opts, awsconfig.WithSharedConfigProfile(strings.TrimSpace(c.cfg.AWSProfile)))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("bedrock: loading AWS config: %w", err)
	}
	return awsCfg, nil
}

// newBedrockClient builds a bedrockruntime client from the resolved AWS config.
func (c *Client) newBedrockClient(ctx context.Context) (*bedrockruntime.Client, error) {
	awsCfg, err := c.bedrockAWSConfig(ctx)
	if err != nil {
		return nil, err
	}
	return bedrockruntime.NewFromConfig(awsCfg), nil
}

// bedrockBody marshals the Anthropic Messages body for Bedrock: the shared
// request minus the top-level "model" and "stream" fields (Bedrock takes the
// model in the API call and always streams via the response-stream API), plus
// the required "anthropic_version".
func bedrockBody(body *request) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	delete(m, "model")
	delete(m, "stream")
	m["anthropic_version"], _ = json.Marshal(bedrockAnthropicVersion)
	return json.Marshal(m)
}

// streamBedrock sends one Messages request to Bedrock and streams the response,
// reusing the Anthropic wire body and the shared SSE parser.
func (c *Client) streamBedrock(ctx context.Context, system string, messages []Message, tools []Tool, h Handler) (*Response, error) {
	bc, err := c.newBedrockClient(ctx)
	if err != nil {
		return nil, err
	}
	model := c.bedrockModelID()

	// Bedrock can't round-trip Anthropic thinking-block signatures the way the
	// first-party API does, so strip replayed thinking blocks (stripThinking).
	body := c.buildAnthropicBody(ctx, model, system, messages, tools, true)
	payload, err := bedrockBody(body)
	if err != nil {
		return nil, err
	}
	dbg(">>> bedrock request (%s): %s", model, payload)

	out, err := bc.InvokeModelWithResponseStream(ctx, &bedrockruntime.InvokeModelWithResponseStreamInput{
		ModelId:     aws.String(model),
		Body:        payload,
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
	})
	if err != nil {
		return nil, fmt.Errorf("bedrock: %w", bedrockError(err))
	}

	// The SDK decodes the AWS event-stream framing and hands us one chunk per
	// Anthropic event (raw JSON). Re-frame each as an SSE "data:" line so the
	// existing parseStream can consume it unchanged; run it on a pipe so parsing
	// happens concurrently with receiving.
	pr, pw := io.Pipe()
	stream := out.GetStream()
	go func() {
		defer stream.Close()
		var perr error
		for ev := range stream.Events() {
			chunk, ok := ev.(*brtypes.ResponseStreamMemberChunk)
			if !ok {
				continue
			}
			if _, werr := fmt.Fprintf(pw, "data: %s\n\n", chunk.Value.Bytes); werr != nil {
				perr = werr
				break
			}
		}
		if perr == nil {
			perr = stream.Err()
		}
		pw.CloseWithError(perr)
	}()

	resp, perr := parseStream(pr, h)
	pr.CloseWithError(perr) // unblock the goroutine if parsing stopped early
	if perr != nil {
		return nil, fmt.Errorf("bedrock: %w", perr)
	}
	return resp, nil
}

// bedrockError unwraps common Bedrock/AWS errors into a friendlier message.
func bedrockError(err error) error {
	var access *brtypes.AccessDeniedException
	if errors.As(err, &access) {
		return fmt.Errorf("access denied — check your AWS credentials, region (MOONY_AWS_REGION), and that the model is enabled in your Bedrock account: %w", err)
	}
	var notFound *brtypes.ResourceNotFoundException
	if errors.As(err, &notFound) {
		return fmt.Errorf("model not found — verify the model / inference-profile id and region: %w", err)
	}
	var throttle *brtypes.ThrottlingException
	if errors.As(err, &throttle) {
		return fmt.Errorf("throttled by Bedrock — slow down or request a quota increase: %w", err)
	}
	return err
}

// checkBedrockAuth verifies Bedrock credentials resolve, without a paid
// inference call. A bad model/region surfaces on first use.
func (c *Client) checkBedrockAuth(ctx context.Context) error {
	_, err := c.bedrockCredentials(ctx)
	return err
}

// bedrockCredentials forces AWS credential resolution so auth problems surface
// at check time rather than on first inference.
func (c *Client) bedrockCredentials(ctx context.Context) (aws.Credentials, error) {
	awsCfg, err := c.bedrockAWSConfig(ctx)
	if err != nil {
		return aws.Credentials{}, err
	}
	creds, err := awsCfg.Credentials.Retrieve(ctx)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("bedrock: no usable AWS credentials — set MOONY_AWS_ACCESS_KEY_ID/MOONY_AWS_SECRET_ACCESS_KEY, MOONY_AWS_PROFILE, or a standard AWS credential source: %w", err)
	}
	return creds, nil
}
