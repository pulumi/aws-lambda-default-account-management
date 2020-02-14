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

const policyAttachment = new aws.iam.PolicyAttachment("my-attachment", {
    policyArn: lambdaRolePolicy.arn,
    roles: [lambdaRole],
});

const attachCloudwatchLogs = new aws.iam.PolicyAttachment("cloudwatch-attachment", {
    policyArn: aws.iam.ManagedPolicies.AWSLambdaBasicExecutionRole,
    roles: [lambdaRole]
});

export const iamArn = lambdaRole.arn;
