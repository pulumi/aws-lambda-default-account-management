package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

// version is stamped at build time by the Makefile (-X main.version). The
// deployment artifact is built by hand and not tracked, so this log line is
// the only way to tell which commit a given region is running.
var version = "unknown"

// CreateEnabled gates every mutating call. It defaults to true so that an
// unset variable preserves the job's behaviour; set CREATE_ENABLED=false to
// exercise the discovery paths against a live account without changing it.
var CreateEnabled = true

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

	log.Printf("Checking default account setup in %q (build %s, create enabled: %t)",
		cfg.Region, version, CreateEnabled)
	return reconcile(ctx, ec2.NewFromConfig(cfg))
}

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
		// CreateDefaultVpc also creates a default subnet in every availability
		// zone, so there is nothing left to reconcile. Returning here rather
		// than falling through matters: DescribeSubnets is eventually
		// consistent, so the read-back would miss those subnets and every
		// zone would get a CreateDefaultSubnet that fails as a conflict.
		log.Printf("Created default VPC %s and its default subnets", vpcID)
		return nil
	}

	zones, err := usableZones(ctx, client)
	if err != nil {
		return err
	}

	covered, err := zonesWithDefaultSubnet(ctx, client, vpcID)
	if err != nil {
		return err
	}

	var createErrs []error
	attempted := 0
	for _, zone := range zones {
		if covered[zone] {
			continue
		}
		if !CreateEnabled {
			log.Printf("Availability zone %s has no default subnet; creation disabled", zone)
			continue
		}
		log.Printf("Creating default subnet in availability zone %s", zone)
		attempted++
		if _, err := client.CreateDefaultSubnet(ctx, &ec2.CreateDefaultSubnetInput{
			AvailabilityZone: aws.String(zone),
		}); err != nil && !alreadyExists(err) {
			// One zone's refusal must not stop the others: CreateDefaultSubnet
			// can decline a single zone on capacity or eligibility grounds.
			log.Printf("error creating default subnet in %s: %s", zone, err)
			createErrs = append(createErrs, fmt.Errorf("%s: %w", zone, err))
		}
	}
	if len(createErrs) > 0 {
		return fmt.Errorf("%d of %d default subnet creations failed: %w",
			len(createErrs), attempted, errors.Join(createErrs...))
	}
	return nil
}

// alreadyExists reports whether err is EC2 declining to create something that
// is already there. A concurrent run, or a retry after a timed-out call that
// in fact succeeded, is not a failure.
func alreadyExists(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "InvalidSubnet.Conflict", "DefaultSubnetAlreadyExistsInAvailabilityZone":
		return true
	}
	return false
}

// defaultVPC returns the region's default VPC id, or "" when there is none.
func defaultVPC(ctx context.Context, client ec2API) (string, error) {
	var token *string
	for {
		out, err := client.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
			Filters:   []ec2types.Filter{{Name: aws.String("is-default"), Values: []string{"true"}}},
			NextToken: token,
		})
		if err != nil {
			return "", fmt.Errorf("describing VPCs: %w", err)
		}
		for _, vpc := range out.Vpcs {
			if id := aws.ToString(vpc.VpcId); id != "" {
				return id, nil
			}
		}
		if aws.ToString(out.NextToken) == "" {
			return "", nil
		}
		token = out.NextToken
	}
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

// zonesWithDefaultSubnet returns the availability zones that already hold a
// default subnet in vpcID, following pagination. The default-for-az filter is
// what makes this correct: a zone may hold many subnets, but at most one of
// them is the default.
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
		if aws.ToString(out.NextToken) == "" {
			return covered, nil
		}
		token = out.NextToken
	}
}
