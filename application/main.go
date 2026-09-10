package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// CreateEnabled gates every mutating call. It defaults to true so that an
// unset variable preserves the job's behaviour; set CREATE_ENABLED=false to
// exercise the discovery paths against a live account without changing it.
var CreateEnabled = true

// ec2API is the subset of *ec2.Client this program uses. Depending on the
// interface rather than the concrete client is what lets the tests drive the
// paging and error paths.
type ec2API interface {
	DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	CreateDefaultVpc(context.Context, *ec2.CreateDefaultVpcInput, ...func(*ec2.Options)) (*ec2.CreateDefaultVpcOutput, error)
	DescribeAvailabilityZones(context.Context, *ec2.DescribeAvailabilityZonesInput, ...func(*ec2.Options)) (*ec2.DescribeAvailabilityZonesOutput, error)
	DescribeSubnets(context.Context, *ec2.DescribeSubnetsInput, ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	CreateDefaultSubnet(context.Context, *ec2.CreateDefaultSubnetInput, ...func(*ec2.Options)) (*ec2.CreateDefaultSubnetOutput, error)
}

func main() {
	if v, ok := os.LookupEnv("CREATE_ENABLED"); ok {
		enabled, err := strconv.ParseBool(v)
		if err != nil {
			log.Fatalf("CREATE_ENABLED=%q is not a boolean: %s", v, err)
		}
		CreateEnabled = enabled
	}
	lambda.Start(HandleRequest)
}

func HandleRequest(ctx context.Context) error {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}

	log.Printf("Checking default account setup in %q (create enabled: %t)", cfg.Region, CreateEnabled)
	return reconcile(ctx, ec2.NewFromConfig(cfg))
}

// reconcile ensures the region has a default VPC and a default subnet in every
// usable availability zone.
func reconcile(ctx context.Context, client ec2API) error {
	vpcID, err := defaultVPC(ctx, client)
	if err != nil {
		return err
	}

	if vpcID == "" {
		if !CreateEnabled {
			log.Print("No default VPC; creation disabled, nothing further to check")
			return nil
		}
		log.Print("No default VPC found, creating one")
		if vpcID, err = createDefaultVPC(ctx, client); err != nil {
			return err
		}
		log.Printf("Created default VPC %s", vpcID)
	}

	zones, err := usableZones(ctx, client)
	if err != nil {
		return err
	}

	covered, err := zonesWithDefaultSubnet(ctx, client, vpcID)
	if err != nil {
		return err
	}

	createErrs := 0
	for _, zone := range zones {
		if covered[zone] {
			continue
		}
		if !CreateEnabled {
			log.Printf("Availability zone %s has no default subnet; creation disabled", zone)
			continue
		}
		log.Printf("Creating default subnet in availability zone %s", zone)
		if _, err := client.CreateDefaultSubnet(ctx, &ec2.CreateDefaultSubnetInput{
			AvailabilityZone: aws.String(zone),
		}); err != nil {
			// One unusable zone must not stop the others; a local-zone or
			// capacity refusal here is routine and self-corrects.
			log.Printf("error creating default subnet in %s: %s", zone, err)
			createErrs++
		}
	}
	if createErrs > 0 {
		return fmt.Errorf("%d of %d default subnet creations failed", createErrs, len(zones))
	}
	return nil
}

// defaultVPC returns the region's default VPC id, or "" when there is none.
func defaultVPC(ctx context.Context, client ec2API) (string, error) {
	out, err := client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []ec2types.Filter{{Name: aws.String("is-default"), Values: []string{"true"}}},
	})
	if err != nil {
		return "", fmt.Errorf("describing VPCs: %w", err)
	}
	if len(out.Vpcs) == 0 {
		return "", nil
	}
	return aws.ToString(out.Vpcs[0].VpcId), nil
}

func createDefaultVPC(ctx context.Context, client ec2API) (string, error) {
	out, err := client.CreateDefaultVpc(ctx, &ec2.CreateDefaultVpcInput{})
	if err != nil {
		return "", fmt.Errorf("creating default VPC: %w", err)
	}
	if out.Vpc == nil || aws.ToString(out.Vpc.VpcId) == "" {
		return "", fmt.Errorf("creating default VPC: response carried no VPC id")
	}
	return aws.ToString(out.Vpc.VpcId), nil
}

// usableZones lists the availability zones a default subnet can be created in.
// Local and Wavelength zones are excluded: CreateDefaultSubnet rejects them,
// and an opted-out zone cannot hold a subnet at all.
func usableZones(ctx context.Context, client ec2API) ([]string, error) {
	out, err := client.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("state"), Values: []string{"available"}},
			{Name: aws.String("zone-type"), Values: []string{"availability-zone"}},
			{Name: aws.String("opt-in-status"), Values: []string{"opt-in-not-required", "opted-in"}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("describing availability zones: %w", err)
	}
	zones := make([]string, 0, len(out.AvailabilityZones))
	for _, az := range out.AvailabilityZones {
		if name := aws.ToString(az.ZoneName); name != "" {
			zones = append(zones, name)
		}
	}
	return zones, nil
}

// zonesWithDefaultSubnet reports which zones already hold a default subnet.
// The default-for-az filter is what makes this correct: a zone may hold many
// subnets, but at most one of them is the default.
func zonesWithDefaultSubnet(ctx context.Context, client ec2API, vpcID string) (map[string]bool, error) {
	covered := map[string]bool{}
	var token *string
	for {
		out, err := client.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
			Filters: []ec2types.Filter{
				{Name: aws.String("vpc-id"), Values: []string{vpcID}},
				{Name: aws.String("default-for-az"), Values: []string{"true"}},
			},
			NextToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("describing subnets: %w", err)
		}
		for _, subnet := range out.Subnets {
			covered[aws.ToString(subnet.AvailabilityZone)] = true
		}
		if out.NextToken == nil || aws.ToString(out.NextToken) == "" {
			return covered, nil
		}
		token = out.NextToken
	}
}
