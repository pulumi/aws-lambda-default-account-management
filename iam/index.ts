import * as aws from "@pulumi/aws";

const lambdaRole = new aws.iam.Role("my-lambda-role", {
    name: "lambda-role-for-account-default-management",
    assumeRolePolicy: aws.iam.assumeRolePolicyForPrincipal({ Service: "lambda.amazonaws.com" }),
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
