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

// RolePolicyAttachment, not PolicyAttachment: the latter is exclusive and owns
// the whole account's principal list for a policy ARN, so it revokes
// attachments it does not manage. Three stacks attach
// AWSLambdaBasicExecutionRole in this account, and whichever applied last was
// silently detaching the other two.
const attachCloudwatchLogs = new aws.iam.RolePolicyAttachment("cloudwatch-attachment", {
    policyArn: aws.iam.ManagedPolicies.AWSLambdaBasicExecutionRole,
    role: lambdaRole.name,
});

export const iamArn = lambdaRole.arn;
