# AWS Lambda Default Account Management

This is an AWS Lambda, written in Go, that can be deployed using Pulumi to a list of regions in an AWS Account that will
ensure that the default VPC and all of it's associated Subnets exist in a region. 

This repository is made up of 3 parts:

* Application
* IAM
* Lambda

## Application

The application can be built from the root of the folder using the command `make buildapp`. This builds the application for
linux/amd64 as a binary named `bootstrap` — required by the `provided.al2023` runtime — and zips it as `deployment.zip`,
verifying the archive's shape before it can be deployed. Go 1.27 or newer is required.

**Build before you deploy.** `deployment.zip` is untracked and `pulumi up` consumes it by path, so a stale artifact deploys
silently and then fails at init in every region. Use `make deploy`, which rebuilds first.

If the region has no default VPC, then a request will be made to the `CreateDefaultVpc` endpoint in the EC2 API. This will
create all of the Subnets, internet gateway and route tables that are usually present in the AWS account

Creating the default VPC also creates the default Subnets, so the run ends there rather than reading them back — the
read would be eventually consistent and every zone would get a redundant request. Any zone it did not cover is picked
up by the next scheduled run.

If the default VPC already exists, the application checks every *usable* availability zone — available, opted-in, and not a
Local or Wavelength zone, since `CreateDefaultSubnet` rejects those — and creates a default Subnet in any that lacks one.

`CREATE_ENABLED=false` makes the run discovery-only: it reports what it would do and changes nothing. Set it through stack
config rather than by editing the function in the console, which is drift the next `pulumi up` reverts:

```bash
cd lambda && pulumi config set createEnabled false && pulumi up
```

Every run logs the commit it was built from (`build <sha>`), because `deployment.zip` is not tracked in git and is built by
hand. That log line is the only way to tell which build a region is running.

## IAM 

The IAM permissions required for the application to run are as follows:

* ec2:DescribeVpcs
* ec2:DescribeAvailabilityZones
* ec2:DescribeSubnets
* ec2:CreateDefaultVpc
* ec2:CreateDefaultSubnet

To deploy the IAM to the account there are a number of steps that are required. These steps need to be run as follows:

1. ```bash
   cd iam
   ```

1. ```bash
   npm install
   ```
   
1. ```bash
   pulumi stack select pulumi/default-account-iam/ci
   ```

1. ```bash
   pulumi up
   ```

This will deploy the permissions required for the Lambda to the AWS account

## Lambda

The Lambda pulumi stack takes a [stack reference](https://www.pulumi.com/docs/concepts/stack/#stackreferences) to the IAM
stack, which is where it reads the execution role's ARN from. To deploy the lambda:

1. ```bash
   cd lambda
   ```

1. ```bash
   npm install
   ```
   
1. ```bash
   pulumi stack select pulumi/default-account-lambda/ci
   ```

1. ```bash
   cd .. && make deploy
   ```

`iamStackName` and `createEnabled` are already set in `Pulumi.ci.yaml`; `iamStackName` is required and must name the IAM
project including its organization (`pulumi/default-account-iam`).

The lambda is scheduled to run daily at 1200 UTC in nine regions and pushes logs to CloudWatch with 30-day retention.

## Operational notes

### Rolling back

A plain `git revert` of the runtime migration is **not deployable**: it restores a program declaring the `go1.x` runtime,
which AWS has refused to create since 2024-02-08. Roll back in one of these ways instead, in increasing order of effort:

1. Disable the schedule (`aws events disable-rule --name run-account-defaults-lambda-every-day --region <region>`) — stops
   the job without touching the function.
2. Set `createEnabled` to `false` and apply — the job still runs and reports, but changes nothing.
3. Revert `application/main.go` only, keeping `provided.al2023` and the `bootstrap` handler, then `make deploy`.

### Stack state

These stacks were re-imported in September 2026 from an abandoned state that lived in an unreachable `stack72`
organization. That old state still nominally claims the same physical resources; it cannot be reached to be destroyed, and
nothing should ever be applied from it. The current state is `pulumi/default-account-iam/ci` and
`pulumi/default-account-lambda/ci`.

If either stack's state is lost, the resources are adoptable again by their physical names — the role
`lambda-role-for-account-default-management`, the policy of the same name, and per region the function
`lambda-for-account-default-management`, the rule `run-account-defaults-lambda-every-day`, its `lambda` target, the
`AllowExecutionFromCloudWatch` permission, and the log group `/aws/lambda/lambda-for-account-default-management`.
