# CI: Publishing container images with GitHub OIDC (no static AWS keys)

`/.github/workflows/release.yml` publishes the `crane-api` and `dominator`
container images to both GHCR and Amazon ECR on every release. GHCR uses the
workflow's built-in `GITHUB_TOKEN`. ECR uses **GitHub OIDC federation** — the
workflow assumes an AWS IAM role for the duration of the job, so no long-lived
`AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY` secrets are stored in the repo.

This document covers the one-time AWS/GitHub setup.

## 1. Add GitHub as an IAM OIDC identity provider

In the AWS account that hosts the ECR repositories, add an identity provider
(IAM → Identity providers → Add provider):

- **Provider type:** OpenID Connect
- **Provider URL:** `https://token.actions.githubusercontent.com`
- **Audience:** `sts.amazonaws.com`

## 2. Create the IAM role the workflow assumes

Create an IAM role with the following trust policy. It allows only workflow runs
on the `main` branch of `nabutabu/crane-oss` to assume it (`sub` scoping is the
important part — do not trust the whole repo).

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::<AWS_ACCOUNT_ID>:oidc-provider/token.actions.githubusercontent.com"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "token.actions.githubusercontent.com:aud": "sts.amazonaws.com"
        },
        "StringLike": {
          "token.actions.githubusercontent.com:sub": "repo:nabutabu/crane-oss:ref:refs/heads/main"
        }
      }
    }
  ]
}
```

Attach a least-privilege inline policy granting ECR push/pull for the two
repositories. `ecr:GetAuthorizationToken` must target `*` (AWS requirement);
the rest are scoped to the repositories.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["ecr:GetAuthorizationToken"],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": [
        "ecr:BatchCheckLayerAvailability",
        "ecr:BatchGetImage",
        "ecr:CompleteLayerUpload",
        "ecr:GetDownloadUrlForLayer",
        "ecr:InitiateLayerUpload",
        "ecr:PutImage",
        "ecr:UploadLayerPart"
      ],
      "Resource": [
        "arn:aws:ecr:<AWS_REGION>:<AWS_ACCOUNT_ID>:repository/crane-oss",
        "arn:aws:ecr:<AWS_REGION>:<AWS_ACCOUNT_ID>:repository/crane-oss-dominator"
      ]
    }
  ]
}
```

## 3. Configure repository variables

The workflow reads two **repository variables** (Settings → Secrets and
variables → Actions → Variables) — not secrets, since neither is sensitive:

| Variable | Example | Purpose |
|----------|---------|---------|
| `AWS_ROLE_ARN` | `arn:aws:iam::123456789012:role/gha-crane-oss-release` | Role the workflow assumes via OIDC. |
| `AWS_REGION` | `us-west-2` | Region for the ECR login/registry. |

## 4. Verify

Trigger the **Release** workflow (`workflow_dispatch`) and confirm the `images`
job logs `Assuming role ... with web identity` and pushes to ECR, with no
reference to static AWS keys.

## Notes

- The ECR repositories `crane-oss` (crane-api) and `crane-oss-dominator`
  (dominator) must already exist; the workflow does not create them.
- Images are tagged `<version>`, `<sha>`, and `latest`, and built for
  `linux/amd64` and `linux/arm64`. Each build's immutable digest is written to
  the job summary so deployments can pin by digest.
