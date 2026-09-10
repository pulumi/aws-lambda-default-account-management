import * as pulumi from "@pulumi/pulumi";
import * as aws from "@pulumi/aws";

const config = new pulumi.Config();
const iamStackName = config.require("iamStackName");
const stackRef = new pulumi.StackReference(`${iamStackName}/${pulumi.getStack()}`);

const lambdaName = "lambda-for-account-default-management";

// Owner is load-bearing, not documentation: pulumi/aws-account-cleanup deletes
// resources in this account that carry no Owner tag, matching the key
// case-insensitively. Removing it from any resource here schedules that
// resource for deletion.
function stackIDTagOrganization(): string {
    const name = pulumi.getOrganization();
    return name === "organization" || name === "pulumi-corp" ? "pulumi" : name;
}

const tags = {
    "Owner": "github.com/pulumi/aws-lambda-default-account-management/lambda",
    "Purpose": "DefaultAccountManagement",
    "pulumi:stack-id": `${stackIDTagOrganization()}/${pulumi.getProject()}/${pulumi.getStack()}`,
};

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
        tags,
    }, {provider});

    const lambda = new aws.lambda.Function(`my-lambda-function-${providerKey}`, {
        name: lambdaName,
        runtime: aws.lambda.Runtime.CustomAL2023,
        architectures: ["x86_64"],
        timeout: 900,
        role: stackRef.getOutput("iamArn"),
        handler: "bootstrap",
        code: new pulumi.asset.FileArchive("../deployment.zip"),
        environment: {
            variables: {
                // Set to "false" to exercise the job against a live account
                // without creating anything.
                CREATE_ENABLED: "true",
            },
        },
        tags,
    }, {provider});

    const lambdaPermission = new aws.lambda.Permission(`allow-cloudwatch-to-trigger-${providerKey}`, {
        statementId: "AllowExecutionFromCloudWatch",
        action: "lambda:InvokeFunction",
        function: lambda,
        principal: "events.amazonaws.com",
        sourceArn: eventRule.arn,
    }, {provider});

    const target = new aws.cloudwatch.EventTarget(`check-account-defaults-lambda-event-${providerKey}`, {
        rule: eventRule.name,
        targetId: "lambda",
        arn: lambda.arn,
    }, {provider});
}
