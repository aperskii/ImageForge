#!/usr/bin/env sh
# Create the AWS resources ImageForge needs.
#
# LocalStack runs this itself on startup, because docker-compose mounts it into
# /etc/localstack/init/ready.d. It is also safe to run by hand at any time --
# `make aws-init` does exactly that -- since every step is idempotent.
#
# Inside the LocalStack container `awslocal` is on the PATH and already points
# at the local endpoint. Anywhere else, plain `aws` is used with the endpoint
# and credentials taken from the environment.
set -eu

REGION="${AWS_REGION:-eu-west-1}"
ENDPOINT="${AWS_ENDPOINT_URL:-http://localhost:4566}"
BUCKET="${IMAGEFORGE_S3_BUCKET:-imageforge-media}"
QUEUE="${IMAGEFORGE_SQS_QUEUE:-imageforge-jobs}"
DLQ="${IMAGEFORGE_SQS_DLQ:-${QUEUE}-dlq}"
TABLE="${IMAGEFORGE_DYNAMODB_TABLE:-imageforge-jobs}"

# How many times a message may be received before it is moved to the DLQ. A
# poison message that kills the worker mid-job reappears each time its
# visibility timeout expires, so this bounds the damage it can do.
MAX_RECEIVE_COUNT="${IMAGEFORGE_SQS_MAX_RECEIVE:-5}"

if command -v awslocal >/dev/null 2>&1; then
	aws_cli() { awslocal "$@"; }
else
	# LocalStack accepts any credentials, but the CLI refuses to sign without.
	export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-test}"
	export AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-test}"
	aws_cli() { aws --endpoint-url "$ENDPOINT" --region "$REGION" "$@"; }
fi

log() { printf '[imageforge-init] %s\n' "$1"; }

# ----------------------------------------------------------------- S3 --------
if aws_cli s3api head-bucket --bucket "$BUCKET" >/dev/null 2>&1; then
	log "bucket $BUCKET already exists"
else
	# us-east-1 is the one region that rejects a LocationConstraint.
	if [ "$REGION" = "us-east-1" ]; then
		aws_cli s3api create-bucket --bucket "$BUCKET" >/dev/null
	else
		aws_cli s3api create-bucket \
			--bucket "$BUCKET" \
			--create-bucket-configuration "LocationConstraint=$REGION" >/dev/null
	fi
	log "created bucket $BUCKET"
fi

# Originals are inputs we can always re-derive results from, and results are
# cache-like. Expiring both keeps a long-running local stack from growing
# without bound.
aws_cli s3api put-bucket-lifecycle-configuration \
	--bucket "$BUCKET" \
	--lifecycle-configuration '{
		"Rules": [
			{"ID": "expire-originals", "Status": "Enabled",
			 "Filter": {"Prefix": "originals/"}, "Expiration": {"Days": 30}},
			{"ID": "expire-results", "Status": "Enabled",
			 "Filter": {"Prefix": "results/"}, "Expiration": {"Days": 90}}
		]
	}' >/dev/null 2>&1 || log "lifecycle rules not applied (unsupported here)"

# ---------------------------------------------------------------- SQS --------
# The dead-letter queue must exist before the main queue can name it in a
# redrive policy.
DLQ_URL="$(aws_cli sqs create-queue --queue-name "$DLQ" --query QueueUrl --output text)"
DLQ_ARN="$(aws_cli sqs get-queue-attributes \
	--queue-url "$DLQ_URL" \
	--attribute-names QueueArn \
	--query 'Attributes.QueueArn' --output text)"
log "dead-letter queue ready: $DLQ_ARN"

QUEUE_URL="$(aws_cli sqs create-queue \
	--queue-name "$QUEUE" \
	--attributes "$(printf '{
		"VisibilityTimeout": "120",
		"MessageRetentionPeriod": "345600",
		"ReceiveMessageWaitTimeSeconds": "20",
		"RedrivePolicy": "{\\"deadLetterTargetArn\\":\\"%s\\",\\"maxReceiveCount\\":\\"%s\\"}"
	}' "$DLQ_ARN" "$MAX_RECEIVE_COUNT")" \
	--query QueueUrl --output text)"
log "queue ready: $QUEUE_URL"

# ----------------------------------------------------------- DynamoDB --------
if aws_cli dynamodb describe-table --table-name "$TABLE" >/dev/null 2>&1; then
	log "table $TABLE already exists"
else
	aws_cli dynamodb create-table \
		--table-name "$TABLE" \
		--attribute-definitions AttributeName=id,AttributeType=S \
		--key-schema AttributeName=id,KeyType=HASH \
		--billing-mode PAY_PER_REQUEST >/dev/null
	aws_cli dynamodb wait table-exists --table-name "$TABLE"
	log "created table $TABLE"
fi

log "ready: bucket=$BUCKET queue=$QUEUE dlq=$DLQ table=$TABLE region=$REGION"
