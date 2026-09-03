# Roles GitHub Actions assumes, over OIDC rather than with stored keys.
#
# The security of this rests entirely on the `sub` condition in each trust
# policy. GitHub signs a token for every workflow run, and that token's subject
# says which repository, branch and environment it came from. Without a
# condition on it, *any* repository on GitHub could assume these roles.
#
# Three roles rather than one, because they need very different power: pushing
# an image, reading state to plan, and changing infrastructure are not the same
# job and should not share a credential.

locals {
  github_host = "token.actions.githubusercontent.com"
  repo_sub    = "repo:${var.github_owner}/${var.github_repository}"
}

# The provider is account-wide, so an account that already has one should pass
# its ARN in rather than creating a second.
resource "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 1 : 0

  url            = "https://${local.github_host}"
  client_id_list = ["sts.amazonaws.com"]

  # AWS verifies GitHub's certificate chain against its own trust store for
  # this provider, so the thumbprint is no longer load-bearing. It is still a
  # required field.
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1"]

  tags = var.tags
}

locals {
  provider_arn = var.create_oidc_provider ? aws_iam_openid_connect_provider.github[0].arn : var.oidc_provider_arn
}

# --------------------------------------------------------------- push role --
# Assumed by the build-and-push workflow. Only from the default branch: a pull
# request from a fork must not be able to push an image into the registry the
# services run from.
data "aws_iam_policy_document" "push_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.github_host}:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.github_host}:sub"
      values   = [for branch in var.push_branches : "${local.repo_sub}:ref:refs/heads/${branch}"]
    }
  }
}

resource "aws_iam_role" "push" {
  name                 = "${var.name_prefix}-gha-push"
  description          = "Pushes ImageForge images to ECR from GitHub Actions"
  assume_role_policy   = data.aws_iam_policy_document.push_assume.json
  max_session_duration = 3600

  tags = merge(var.tags, { Purpose = "gha-push" })
}

data "aws_iam_policy_document" "push" {
  # The token for `docker login`. This one cannot be scoped to a repository:
  # the API takes no resource.
  statement {
    sid       = "GetAuthToken"
    effect    = "Allow"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  # Everything else is scoped to this project's repositories. Pushing is
  # allowed; deleting an image is not.
  statement {
    sid    = "PushImages"
    effect = "Allow"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:CompleteLayerUpload",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
    ]
    resources = var.ecr_repository_arns
  }
}

resource "aws_iam_role_policy" "push" {
  name   = "${var.name_prefix}-gha-push"
  role   = aws_iam_role.push.id
  policy = data.aws_iam_policy_document.push.json
}

# --------------------------------------------------------------- plan role --
# Assumed by the terraform workflow on a pull request. Read-only: a plan
# describes what would change and must not be able to change anything.
data "aws_iam_policy_document" "plan_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.github_host}:aud"
      values   = ["sts.amazonaws.com"]
    }

    # Pull requests from within the repository, plus the default branches.
    # StringLike rather than StringEquals because a pull request's subject
    # carries its number.
    condition {
      test     = "StringLike"
      variable = "${local.github_host}:sub"
      values = concat(
        ["${local.repo_sub}:pull_request"],
        [for branch in var.push_branches : "${local.repo_sub}:ref:refs/heads/${branch}"],
      )
    }
  }
}

resource "aws_iam_role" "plan" {
  name                 = "${var.name_prefix}-gha-tf-plan"
  description          = "Reads state and describes changes for terraform plan"
  assume_role_policy   = data.aws_iam_policy_document.plan_assume.json
  max_session_duration = 3600

  tags = merge(var.tags, { Purpose = "gha-terraform-plan" })
}

resource "aws_iam_role_policy_attachment" "plan_readonly" {
  role = aws_iam_role.plan.name
  # A plan has to read every service the configuration touches, which is most
  # of them. ReadOnlyAccess is broad but it is read-only, which is the property
  # that matters here.
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

resource "aws_iam_role_policy" "plan_state" {
  count = var.state_bucket_arn == "" ? 0 : 1

  name = "${var.name_prefix}-gha-tf-plan-state"
  role = aws_iam_role.plan.id

  # A plan writes a lock file, so read-only is not quite enough once remote
  # state is in use.
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid      = "LockState"
      Effect   = "Allow"
      Action   = ["s3:PutObject", "s3:DeleteObject"]
      Resource = ["${var.state_bucket_arn}/*"]
    }]
  })
}

# -------------------------------------------------------------- apply role --
# Assumed by the terraform workflow's apply job. Scoped to a GitHub
# *environment* rather than a branch, which is what makes the approval rule on
# that environment mean something: without the approval, no token with this
# subject is ever minted.
data "aws_iam_policy_document" "apply_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.github_host}:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.github_host}:sub"
      values   = [for env in var.apply_environments : "${local.repo_sub}:environment:${env}"]
    }
  }
}

resource "aws_iam_role" "apply" {
  name                 = "${var.name_prefix}-gha-tf-apply"
  description          = "Applies ImageForge infrastructure from an approved workflow run"
  assume_role_policy   = data.aws_iam_policy_document.apply_assume.json
  max_session_duration = 3600

  tags = merge(var.tags, { Purpose = "gha-terraform-apply" })
}

resource "aws_iam_role_policy_attachment" "apply" {
  role = aws_iam_role.apply.name
  # Applying this configuration creates IAM roles, so the role that does it
  # needs administrative power. That is the honest reason it sits behind an
  # environment approval instead of running on merge. Narrowing it means
  # enumerating every action across eight services and revisiting that list
  # whenever the configuration grows.
  policy_arn = var.apply_policy_arn
}
