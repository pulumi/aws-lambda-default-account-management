import * as pulumi from "@pulumi/pulumi";
import * as aws from "@pulumi/aws";

// Kept in sync by hand with lambda/index.ts: the two Pulumi projects
// share no module, and pulumi:stack-id is read by external tooling.
function stackIDTagOrganization(): string {
    const name = pulumi.getOrganization();
    return name === "organization" || name === "pulumi-corp" ? "pulumi" : name;
}

// Owner is load-bearing, not documentation: pulumi/aws-account-cleanup deletes
// resources in this account that carry no Owner tag, matching the key
// case-insensitively. Removing it from any resource here schedules that
// resource for deletion.
const tags = {
    "Owner": "github.com/pulumi/aws-lambda-default-account-management/iam",
    "Purpose": "DefaultAccountManagement",
    "pulumi:stack-id": `${stackIDTagOrganization()}/${pulumi.getProject()}/${pulumi.getStack()}`,
};

const lambdaRole = new aws.iam.Role("my-lambda-role", {
    name: "lambda-role-for-account-default-management",
    assumeRolePolicy: aws.iam.assumeRolePolicyForPrincipal({ Service: "lambda.amazonaws.com" }),
    tags,
});

const lambdaRolePolicy = new aws.iam.Policy("my-policy", {
    name: "lambda-policy-for-account-default-management",
    policy: {
        Version: "2012-10-17",
        Statement: [{
            Action: [
                "ec2:DescribeVpcs",
                "ec2:DescribeAvailabilityZones",
                "ec2:DescribeSubnets",
                "ec2:CreateDefaultVpc",
                "ec2:CreateDefaultSubnet",
            ],
            Resource: "*",
            Effect: "Allow",
        }],
    },
    tags,
});

const policyAttachment = new aws.iam.RolePolicyAttachment("my-attachment", {
    policyArn: lambdaRolePolicy.arn,
    role: lambdaRole.name,
});

// A stack-specific policy rather than the AWS-managed
// AWSLambdaBasicExecutionRole. That policy is contended in this account: five
// stacks attached it with aws.iam.PolicyAttachment, the exclusive form, which
// owns the policy's entire principal list and revokes attachments it does not
// manage. This role lost it that way, which is why these nine Lambdas have not
// written a log line since 2020-04-22.
//
// Two of the contending stacks live in archived repositories and still name
// the managed policy, so no change here can stop them. Not using the shared
// ARN is what makes that irrelevant.
const cloudwatchLogs = new aws.iam.RolePolicy("cloudwatch-logs", {
    role: lambdaRole.name,
    policy: JSON.stringify({
        Version: "2012-10-17",
        Statement: [{
            Effect: "Allow",
            Action: ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"],
            Resource: "*",
        }],
    }),
});

export const iamArn = lambdaRole.arn;
