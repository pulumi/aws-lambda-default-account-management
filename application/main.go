package main

import (
	"context"
	"errors"
	"log"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/external"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func main() {
	lambda.Start(HandleRequest)
}

func HandleRequest() {
	cfg, err := external.LoadDefaultAWSConfig()
	if err != nil {
		log.Print("loading AWS config failed")
		log.Fatal(err)
	}

	log.Printf("Checking Default AWS Account Setup in %q", cfg.Region)

	svc := ec2.New(cfg)
	req := svc.DescribeVpcsRequest(&ec2.DescribeVpcsInput{
		MaxResults: aws.Int64(1000),
	})
	res, err := req.Send(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	hasDefaultVpc := false
	defaultVpcId := ""
	for _, vpc := range res.Vpcs {
		if *vpc.IsDefault {
			hasDefaultVpc = true
			defaultVpcId = *vpc.VpcId
		}
	}

	if !hasDefaultVpc {
		log.Printf("Creating default VPC and associated resources for region %q", cfg.Region)
		vpcId, err := createDefaultVpc(svc)
		if err != nil {
			log.Fatal(err)
		}

		defaultVpcId = *vpcId
	}

	azsReq := svc.DescribeAvailabilityZonesRequest(&ec2.DescribeAvailabilityZonesInput{})
	azsRes, err := azsReq.Send(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	for _, az := range azsRes.AvailabilityZones {
		azExists, err := subnetExistsForAz(svc, defaultVpcId, *az.ZoneId)
		if err != nil {
			log.Print(err)
			log.Fatalf("Error retrieving subnet for AZ %q", *az.ZoneName)
		}

		if !azExists {
			log.Printf("Creating Default Subnet for AZ %q", *az.ZoneName)
			if err := createSubnetForAz(svc, az.ZoneName); err != nil {
				log.Fatal(err)
			}
		}
	}
}

func createDefaultVpc(client *ec2.Client) (*string, error) {
	req := client.CreateDefaultVpcRequest(&ec2.CreateDefaultVpcInput{})
	res, err := req.Send(context.Background())
	if err != nil {
		return nil, err
	}

	return res.Vpc.VpcId, nil
}

func createSubnetForAz(client *ec2.Client, azName *string) error {
	req := client.CreateDefaultSubnetRequest(&ec2.CreateDefaultSubnetInput{
		AvailabilityZone: azName,
	})
	_, err := req.Send(context.Background())
	if err != nil {
		return err
	}

	return nil
}

func subnetExistsForAz(client *ec2.Client, vpcID string, zoneID string) (bool, error) {
	subnetReq := client.DescribeSubnetsRequest(&ec2.DescribeSubnetsInput{
		Filters: []ec2.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
			{
				Name:   aws.String("availability-zone-id"),
				Values: []string{zoneID},
			},
		},
	})
	subnetRes, err := subnetReq.Send(context.Background())
	if err != nil {
		return false, err
	}

	if len(subnetRes.Subnets) > 1 {
		return false, errors.New("found more than 1 default VPC for the AZ")
	}

	if len(subnetRes.Subnets) == 0 {
		return false, nil
	}

	return true, nil
}
