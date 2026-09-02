# ImageForge on AWS

Terraform for a real deployment: S3, SQS, DynamoDB, ECR, ECS Fargate and
CloudFront, wired together by a per-environment root module.

```
deployments/terraform/
  modules/
    s3/          raw and processed buckets, with lifecycle rules
    sqs/         job queue, dead-letter queue, redrive policy, DLQ alarm
    dynamodb/    job table, on-demand billing
    ecr/         one repository per image, with expiry rules
    network/     VPC, subnets, and optionally a NAT gateway
    cloudfront/  distribution over the results, with an origin access control
    ecs/         cluster, load balancer, task definitions, services, IAM
  environments/
    dev/         wires the modules together for one environment
```

Each module is independent and takes a `name_prefix`, so a second environment is
another directory under `environments/` rather than a copy of anything.

## Running it

You need Terraform 1.9+ and credentials for an account you are willing to create
billable resources in.

```sh
cd deployments/terraform/environments/dev

terraform init
terraform plan -out=tfplan     # read this before applying
terraform apply tfplan
```

`plan` is worth actually reading: the first apply creates about sixty resources,
including a load balancer and a CloudFront distribution that take several
minutes each.

To tear it down:

```sh
terraform destroy
```

The dev environment sets `force_destroy` on its buckets and ECR repositories, so
`destroy` works without emptying them by hand first. That is deliberately not
the default in the modules, because it is the wrong behaviour anywhere else.

### State

State is local, which is fine for one person and wrong for a team: a local state
file cannot be locked, so two applies at once corrupt it. `versions.tf` has a
commented S3 backend to fill in before a second person touches the environment.

### After the first apply

The services will not start until images exist, because the task definitions
point at `:latest` in repositories that begin empty. Build, push, then roll:

From the repository root, with `TF=deployments/terraform/environments/dev`:

```sh
REGISTRY=$(terraform -chdir=$TF output -json ecr_repository_urls | jq -r '.api | split("/")[0]')
REGION=$(terraform -chdir=$TF output -raw aws_region)

aws ecr get-login-password --region "$REGION" \
  | docker login --username AWS --password-stdin "$REGISTRY"

for svc in api worker; do
  IMAGE=$(terraform -chdir=$TF output -json ecr_repository_urls | jq -r ".$svc"):latest
  docker build -f "deployments/docker/$svc.Dockerfile" -t "$IMAGE" .
  docker push "$IMAGE"
done

# Roll both services onto the images that were just pushed.
eval "$(terraform -chdir=$TF output -raw deploy_command)"
```

`terraform output` gives you the API URL, the CloudFront domain, the bucket
names, the queue URL and the table name.

## What it builds

**Two buckets.** Originals expire after 7 days in dev and results after 30,
because an original is an input a result can be re-derived from and a result is
cache-like. Both block public access entirely, have ACLs disabled, and abort
incomplete multipart uploads after a week — an upload that is never completed is
billed for storage forever and does not appear in a listing.

**A queue and its dead-letter queue**, with a redrive policy moving a message
after five receives, and a `redrive_allow_policy` on the DLQ so it only accepts
dead letters from this queue. A CloudWatch alarm fires when the DLQ is not
empty; wire `alarm_actions` to an SNS topic to be told about it.

**One DynamoDB table**, on-demand. The access pattern is one small write per
upload and a few reads while a client polls: spiky and tiny, which is exactly
what provisioned capacity is bad at.

**Two ECR repositories** with lifecycle rules, because images are the one thing
here that grows without bound if nothing deletes them.

**A Fargate cluster** running the API behind an Application Load Balancer and
the worker behind nothing at all — it reaches SQS and is never reached, so its
security group has no ingress rule.

**A CloudFront distribution** over the results, reaching S3 through an Origin
Access Control so the bucket itself stays private.

### IAM

The two task roles are scoped to what the code does, which is narrower than what
the services are:

| | API | Worker |
| --- | --- | --- |
| S3 | `PutObject` on `originals/*` | `GetObject` on `originals/*`, `PutObject` on `results/*` |
| SQS | `SendMessage`, `GetQueueUrl` | `ReceiveMessage`, `DeleteMessage`, `ChangeMessageVisibility`, `GetQueueUrl`, `GetQueueAttributes` |
| DynamoDB | `PutItem`, `GetItem`, `UpdateItem` | `GetItem`, `UpdateItem` |

The API has no `s3:GetObject` because it never reads an object: `GET /jobs/{id}`
answers from DynamoDB and the browser fetches the result from CloudFront. The
worker has no `dynamodb:PutItem` because it never creates a job. Neither has
`Scan` or `Query`, because the application only ever addresses a job by key.

The execution role — the one the ECS agent uses to pull images and write logs —
is separate from both, so the application never inherits the ability to read
every repository in the account.

The token signing key is generated by Terraform, stored in Parameter Store as a
`SecureString`, and injected through the task definition's `secrets` block. It
is not in the definition itself, where anyone with `ecs:DescribeTaskDefinition`
could read it, and `ignore_changes` keeps a routine plan from rotating it and
invalidating every outstanding token.

## Estimated cost

A low-traffic dev deployment, `eu-west-1`, one task of each running all month,
at on-demand prices as of early 2026. Treat these as the right order of
magnitude rather than a quote — prices vary by region and change.

| Item | Sizing | Monthly (USD) |
| --- | --- | --- |
| Application Load Balancer | 1 ALB, minimal LCUs | ~16.50 |
| Fargate — worker | 0.5 vCPU, 1 GB, 730 h | ~18.00 |
| Fargate — API | 0.25 vCPU, 0.5 GB, 730 h | ~9.00 |
| CloudWatch Logs | ~1 GB ingested, 7-day retention | ~0.60 |
| ECR storage | ~1.5 GB of images | ~0.15 |
| S3 storage + requests | a few GB, low request volume | ~0.20 |
| CloudFront | well inside the 1 TB/month free tier | ~0.00 |
| SQS | well inside the 1 M requests/month free tier | ~0.00 |
| DynamoDB on-demand | thousands of tiny reads and writes | ~0.05 |
| NAT gateway | **not created** (`enable_nat_gateway = false`) | 0.00 |
| **Total** | | **~45** |

Two figures dominate, and both are the price of *running all the time* rather
than of doing any work:

- **Fargate, ~27/month.** Two tasks up 24/7. Scaling the worker to zero outside
  working hours roughly halves it; so does `FARGATE_SPOT`, which the cluster
  already has as a capacity provider.
- **The load balancer, ~16.50/month.** An ALB bills by the hour whether or not
  anything reaches it. There is no cheaper way to get a stable HTTP endpoint in
  front of Fargate; putting the API behind API Gateway instead trades the fixed
  cost for a per-request one, which is better only at genuinely low volume.

The single largest saving is the one already taken: **no NAT gateway**, which
would add about 32/month before data charges and would have been the biggest
line on the bill. Tasks run in public subnets with public IPs instead, reachable
by nothing because their security group accepts only the load balancer. That is
a reasonable trade for dev and the wrong one for production, where
`enable_nat_gateway = true` puts them in private subnets.

To spend nothing while keeping the environment: set `api_desired_count` and
`worker_desired_count` to `0` and apply. The ALB still bills.

## Known gap: one bucket, two buckets

The modules create **raw** and **processed** buckets, as separate storage for
inputs and outputs deserves. The application does not use them that way yet: it
takes a single `IMAGEFORGE_S3_BUCKET` and writes `originals/` and `results/`
into it.

So the dev environment points the application at the **raw** bucket, and puts
CloudFront in front of that same bucket rather than the processed one, because
that is where results actually land. The processed bucket is created and left
empty, ready for the split.

That has one consequence worth being explicit about: the CloudFront origin
bucket also holds private uploads. The distribution's bucket policy is therefore
scoped to `results/*` (`allowed_key_prefix` in the CloudFront module), so an
original cannot be fetched from the CDN by anyone who learns a job id. Without
that scoping this arrangement would leak every upload.

Closing the gap properly needs a second bucket setting in the application —
roughly a `IMAGEFORGE_S3_RESULT_BUCKET` and a storage adapter that routes by key
prefix. Once that exists, point `app_bucket_*` at raw, the CloudFront module at
processed, drop `allowed_key_prefix`, and split the worker's `PutObject` grant
onto the processed bucket.

## Not included

Deliberate omissions, so their absence is not mistaken for an oversight:

- **TLS on the API.** The listener is HTTP on port 80. Production needs an ACM
  certificate, a 443 listener and a redirect, which needs a domain.
- **A domain.** Everything is reachable at AWS-generated names.
- **Autoscaling.** Task counts are fixed. The worker is the obvious candidate,
  scaling on the queue's `ApproximateNumberOfMessagesVisible`.
- **The front-end.** It is a dev server in `docker-compose.yml`. Shipping it
  means a build step and static hosting, most naturally its own CloudFront
  distribution over an S3 bucket.
- **A CI deployment role.** Images are pushed and services rolled by hand.
