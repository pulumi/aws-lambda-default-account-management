# AWS Lambda Default Account Management

This is an AWS Lambda, written in Go, that can be deployed using Pulumi to a list of regions in an AWS Account that will
ensure that the default VPC and all of it's associated Subnets exist in a region. 

This repository is made up of 3 parts:

* Application
* IAM
* Lambda

## Application

The application can be built from the root of the folder using the command `make buildapp`. This will build the application
for Linux and zip it up for deployment as `deployment.zip`

If the region has no default VPC, then a request will be made to the `CreateDefaultVpc` endpoint in the EC2 API. This will
create all of the Subnets, internet gateway and route tables that are usually present in the AWS account

If the default VPC exists, then the application will iterate over all of the availability zones for a region and check that
there is a default Subnet for that availability zone. If a subnet is not present, then the application will make a request
to `CreateDefaultSubnet` endpoint in the EC2 API.

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
   pulumi stack init
   ```
   
1. ```bash
   pulumi config set aws:region us-west-2
   ```
   
1. ```bash
   pulumi up
   ```
   
This will deploy the permissions required for the Lambda to the AWS account

## Lambda

The Lambda pulumi stack has a [stack reference]() to the IAM pulumi stack. This is configured in the code but we can set
the stack reference as a config value. To deploy the lambda, the steps need to be run as follows:

1. ```bash
   cd lambda
   ```

1. ```bash
   npm install
   ```
   
1. ```bash
   pulumi stack init
   ```
   
1. ```bash
   pulumi config set aws:region us-west-2
   ```
   
1. ```bash
   pulumi config set iamStackName <org + iam project name>
   ```
   The stack reference name for my project is `stack72/default-account-iam`
   
1. ```bash
   pulumi up
   ```
 
The lambda is scheduled to run daily at 1200 UTC and pushes logs to Cloudwatch.
