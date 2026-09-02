// Package awscfg builds the AWS clients the adapters are driven by.
//
// Every setting comes from the environment, and an AWS_ENDPOINT_URL override
// points the clients at LocalStack instead of the real service. Nothing here
// knows about jobs or images; it exists so the adapter packages take ready-made
// clients and stay testable.
package awscfg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// Environment variables read by SettingsFromEnv.
const (
	// EnvEndpoint overrides the service endpoint for every client, which is
	// how the stack is pointed at LocalStack.
	EnvEndpoint = "AWS_ENDPOINT_URL"
	// EnvRegion selects the AWS region.
	EnvRegion = "AWS_REGION"
	// EnvBucket names the S3 bucket holding originals and results.
	EnvBucket = "IMAGEFORGE_S3_BUCKET"
	// EnvQueue names the SQS queue carrying job identifiers.
	EnvQueue = "IMAGEFORGE_SQS_QUEUE"
	// EnvTable names the DynamoDB table holding job state.
	EnvTable = "IMAGEFORGE_DYNAMODB_TABLE"
)

// Defaults matching the resources deployments/docker/localstack-init.sh
// creates, so a local stack needs no configuration at all.
const (
	DefaultRegion = "eu-west-1"
	DefaultBucket = "imageforge-media"
	DefaultQueue  = "imageforge-jobs"
	DefaultTable  = "imageforge-jobs"
)

// ErrMissingSetting is returned when a required resource name is empty.
var ErrMissingSetting = errors.New("missing aws setting")

// Settings names the AWS resources the adapters use.
type Settings struct {
	// Region is the AWS region. Defaults to DefaultRegion.
	Region string
	// Endpoint overrides the service endpoint for every client. Empty means
	// the real AWS endpoints.
	Endpoint string
	// Bucket is the S3 bucket for originals and results.
	Bucket string
	// Queue is the SQS queue name carrying job identifiers.
	Queue string
	// Table is the DynamoDB table holding job state.
	Table string
}

// SettingsFromEnv reads the settings from the environment, falling back to the
// defaults that match the local LocalStack resources.
func SettingsFromEnv() Settings {
	return Settings{
		Region:   env(EnvRegion, DefaultRegion),
		Endpoint: env(EnvEndpoint, ""),
		Bucket:   env(EnvBucket, DefaultBucket),
		Queue:    env(EnvQueue, DefaultQueue),
		Table:    env(EnvTable, DefaultTable),
	}
}

// UsesLocalStack reports whether an endpoint override is in effect, meaning the
// clients talk to something other than real AWS.
func (s Settings) UsesLocalStack() bool { return s.Endpoint != "" }

// Validate reports any resource name left empty.
func (s Settings) Validate() error {
	var errs []error
	for _, field := range []struct{ name, value string }{
		{"region", s.Region},
		{"bucket", s.Bucket},
		{"queue", s.Queue},
		{"table", s.Table},
	} {
		if strings.TrimSpace(field.value) == "" {
			errs = append(errs, fmt.Errorf("%w: %s", ErrMissingSetting, field.name))
		}
	}
	return errors.Join(errs...)
}

// Load builds the shared AWS configuration.
//
// When an endpoint override is set the configuration also gets static
// placeholder credentials, because LocalStack accepts any credentials but the
// SDK still refuses to sign a request without some.
func Load(ctx context.Context, settings Settings) (aws.Config, error) {
	opts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(settings.Region),
	}
	if settings.UsesLocalStack() && os.Getenv("AWS_ACCESS_KEY_ID") == "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		))
	}

	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("awscfg: load: %w", err)
	}
	return cfg, nil
}

// S3 builds an S3 client.
//
// Against LocalStack it uses path-style addressing: virtual-host style would
// require a wildcard DNS entry per bucket, which a local endpoint does not
// have.
func S3(cfg aws.Config, settings Settings) *s3.Client {
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		if settings.UsesLocalStack() {
			o.BaseEndpoint = aws.String(settings.Endpoint)
			o.UsePathStyle = true
		}
	})
}

// SQS builds an SQS client.
func SQS(cfg aws.Config, settings Settings) *sqs.Client {
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		if settings.UsesLocalStack() {
			o.BaseEndpoint = aws.String(settings.Endpoint)
		}
	})
}

// DynamoDB builds a DynamoDB client.
func DynamoDB(cfg aws.Config, settings Settings) *dynamodb.Client {
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if settings.UsesLocalStack() {
			o.BaseEndpoint = aws.String(settings.Endpoint)
		}
	})
}

// env returns the environment value for key, or fallback when unset or blank.
func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}
