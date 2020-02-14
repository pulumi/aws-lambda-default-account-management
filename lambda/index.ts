import * as pulumi from "@pulumi/pulumi";
import * as aws from "@pulumi/aws";

const config = new pulumi.Config();
const iamStackName = config.get("iamStackName");
const stackRef = new pulumi.StackReference(`${iamStackName}/${pulumi.getStack()}`);

const providers: {[key: string]: aws.Provider} = {
    "us-east-1": new aws.Provider("us-east-1", {region: "us-east-1"}),
    "us-east-2": new aws.Provider("us-east-2", {region: "us-east-2"}),
    "us-west-1": new aws.Provider("us-west-1", {region: "us-west-1"}),
    "us-west-2": new aws.Provider("us-west-2", {region: "us-west-2"}),
    "eu-west-1": new aws.Provider("eu-west-1", {region: "eu-west-1"}),
    "eu-west-2": new aws.Provider("eu-west-2", {region: "eu-west-2"}),
    "eu-west-3": new aws.Provider("eu-west-3", {region: "eu-west-3"}),
    "eu-central-1": new aws.Provider("eu-central-1", {region: "eu-central-1"}),
    "ap-southeast-2": new aws.Provider("ap-southeast-2", {region: "ap-southeast-2"}),
};

for (const providerKey of Object.keys(providers)) {
    const provider = providers[providerKey];

    const eventRule = new aws.cloudwatch.EventRule(`run-account-defaults-lambda-every-day-${providerKey}`, {
        name: "run-account-defaults-lambda-every-day",
        description: "Rule to trigger AWS Account Default Setup lambda every day at 1200 UTC",
        scheduleExpression: "cron(0 12 * * ? *)",
        tags: {
            "Owner": "Stack72",
            "Purpose": "AccountCleanup",
        }
    }, {provider});

    const lambda = new aws.lambda.Function(`my-lambda-function-${providerKey}`, {
        name: "lambda-for-account-default-management",
        runtime: aws.lambda.Go1dxRuntime,
        timeout: 900,
        role: stackRef.getOutput("iamArn"),
        handler: "main",
        code: new pulumi.asset.FileArchive("../deployment.zip"),
        tags: {
            "Owner": "Stack72",
            "Purpose": "DefaultAccountManagement",
        }
    }, {provider});

    const lambdaPermission = new aws.lambda.Permission(`allow-cloudwatch-to-trigger-${providerKey}`, {
        statementId: "AllowExecutionFromCloudWatch",
        action: "lambda:InvokeFunction",
        function: lambda,
        principal: "events.amazonaws.com",
        sourceArn: eventRule.arn,
    }, {provider})

    const target = new aws.cloudwatch.EventTarget(`check-account-defaults-lambda-event-${providerKey}`, {
        rule: eventRule.name,
        targetId: "lambda",
        arn: lambda.arn,
    }, {provider});
}
