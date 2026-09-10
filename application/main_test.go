package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// fakeEC2 records what it was asked and replays canned answers. Subnet pages
// are served one per DescribeSubnets call so paging is exercised for real.
type fakeEC2 struct {
	vpcs         []ec2types.Vpc
	vpcsErr      error
	createdVPC   string
	createVPCEr  error
	createVPCNil bool
	zones        []string
	zonesErr     error
	subnetPages  [][]ec2types.Subnet
	subnetsErr   error

	describeSubnetCalls int
	createdSubnets      []string
	failSubnetsIn       map[string]bool
	azFilters           []ec2types.Filter
	vpcFilters          []ec2types.Filter
	subnetFilters       []ec2types.Filter
	createVPCCalls      int
}

func (f *fakeEC2) DescribeVpcs(_ context.Context, in *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	f.vpcFilters = in.Filters
	if f.vpcsErr != nil {
		return nil, f.vpcsErr
	}
	return &ec2.DescribeVpcsOutput{Vpcs: f.vpcs}, nil
}

func (f *fakeEC2) CreateDefaultVpc(_ context.Context, _ *ec2.CreateDefaultVpcInput, _ ...func(*ec2.Options)) (*ec2.CreateDefaultVpcOutput, error) {
	f.createVPCCalls++
	if f.createVPCEr != nil {
		return nil, f.createVPCEr
	}
	if f.createVPCNil {
		return &ec2.CreateDefaultVpcOutput{}, nil
	}
	return &ec2.CreateDefaultVpcOutput{Vpc: &ec2types.Vpc{VpcId: aws.String(f.createdVPC)}}, nil
}

func (f *fakeEC2) DescribeAvailabilityZones(_ context.Context, in *ec2.DescribeAvailabilityZonesInput, _ ...func(*ec2.Options)) (*ec2.DescribeAvailabilityZonesOutput, error) {
	f.azFilters = in.Filters
	if f.zonesErr != nil {
		return nil, f.zonesErr
	}
	out := &ec2.DescribeAvailabilityZonesOutput{}
	for _, z := range f.zones {
		out.AvailabilityZones = append(out.AvailabilityZones, ec2types.AvailabilityZone{ZoneName: aws.String(z)})
	}
	return out, nil
}

func (f *fakeEC2) DescribeSubnets(_ context.Context, in *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	f.subnetFilters = in.Filters
	if f.subnetsErr != nil {
		return nil, f.subnetsErr
	}
	page := f.describeSubnetCalls
	f.describeSubnetCalls++
	if page >= len(f.subnetPages) {
		return &ec2.DescribeSubnetsOutput{}, nil
	}
	out := &ec2.DescribeSubnetsOutput{Subnets: f.subnetPages[page]}
	if page+1 < len(f.subnetPages) {
		out.NextToken = aws.String("more")
	}
	return out, nil
}

func (f *fakeEC2) CreateDefaultSubnet(_ context.Context, in *ec2.CreateDefaultSubnetInput, _ ...func(*ec2.Options)) (*ec2.CreateDefaultSubnetOutput, error) {
	az := aws.ToString(in.AvailabilityZone)
	if f.failSubnetsIn[az] {
		return nil, errors.New("boom")
	}
	f.createdSubnets = append(f.createdSubnets, az)
	return &ec2.CreateDefaultSubnetOutput{}, nil
}

func subnetIn(az string) ec2types.Subnet {
	return ec2types.Subnet{AvailabilityZone: aws.String(az)}
}

// withCreateEnabled sets the global for one test and restores it after.
func withCreateEnabled(t *testing.T, v bool) {
	t.Helper()
	prev := CreateEnabled
	CreateEnabled = v
	t.Cleanup(func() { CreateEnabled = prev })
}

func hasFilter(filters []ec2types.Filter, name string, value string) bool {
	for _, f := range filters {
		if aws.ToString(f.Name) != name {
			continue
		}
		for _, v := range f.Values {
			if v == value {
				return true
			}
		}
	}
	return false
}

func TestExistingVpcIsNotRecreated(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{
		vpcs:        []ec2types.Vpc{{VpcId: aws.String("vpc-existing")}},
		zones:       []string{"us-west-2a"},
		subnetPages: [][]ec2types.Subnet{{subnetIn("us-west-2a")}},
	}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if f.createVPCCalls != 0 {
		t.Errorf("created a VPC when one already existed")
	}
	if len(f.createdSubnets) != 0 {
		t.Errorf("created subnets %v when the zone was already covered", f.createdSubnets)
	}
}

func TestMissingVpcIsCreatedAndUsedForSubnetLookup(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{createdVPC: "vpc-new", zones: []string{"us-west-2a"}}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if f.createVPCCalls != 1 {
		t.Fatalf("createVPCCalls = %d, want 1", f.createVPCCalls)
	}
	// The subnet lookup must be scoped to the VPC that was just created, not
	// to the empty id the lookup started with.
	if !hasFilter(f.subnetFilters, "vpc-id", "vpc-new") {
		t.Errorf("subnet lookup filters = %v, want vpc-id=vpc-new", f.subnetFilters)
	}
}

func TestOnlyUncoveredZonesGetSubnets(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{
		vpcs:        []ec2types.Vpc{{VpcId: aws.String("vpc-1")}},
		zones:       []string{"us-west-2a", "us-west-2b", "us-west-2c"},
		subnetPages: [][]ec2types.Subnet{{subnetIn("us-west-2b")}},
	}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	want := []string{"us-west-2a", "us-west-2c"}
	if strings.Join(f.createdSubnets, ",") != strings.Join(want, ",") {
		t.Errorf("createdSubnets = %v, want %v", f.createdSubnets, want)
	}
}

func TestCoverageIsReadAcrossAllSubnetPages(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{
		vpcs:  []ec2types.Vpc{{VpcId: aws.String("vpc-1")}},
		zones: []string{"us-west-2a", "us-west-2b"},
		subnetPages: [][]ec2types.Subnet{
			{subnetIn("us-west-2a")},
			{subnetIn("us-west-2b")},
		},
	}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if f.describeSubnetCalls != 2 {
		t.Errorf("describeSubnetCalls = %d, want 2 (paging not followed)", f.describeSubnetCalls)
	}
	if len(f.createdSubnets) != 0 {
		t.Errorf("created %v; a zone covered on page 2 was treated as uncovered", f.createdSubnets)
	}
}

func TestOneZoneFailureDoesNotStopTheOthers(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{
		vpcs:          []ec2types.Vpc{{VpcId: aws.String("vpc-1")}},
		zones:         []string{"us-west-2a", "us-west-2b", "us-west-2c"},
		failSubnetsIn: map[string]bool{"us-west-2b": true},
	}
	err := reconcile(context.Background(), f)
	if err == nil {
		t.Fatal("reconcile returned nil despite a failed creation")
	}
	if !strings.Contains(err.Error(), "1 of 3") {
		t.Errorf("error %q does not report the failure count", err)
	}
	want := []string{"us-west-2a", "us-west-2c"}
	if strings.Join(f.createdSubnets, ",") != strings.Join(want, ",") {
		t.Errorf("createdSubnets = %v, want %v", f.createdSubnets, want)
	}
}

func TestCreateDisabledMakesNoMutatingCalls(t *testing.T) {
	withCreateEnabled(t, false)
	f := &fakeEC2{
		vpcs:  []ec2types.Vpc{{VpcId: aws.String("vpc-1")}},
		zones: []string{"us-west-2a", "us-west-2b"},
	}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(f.createdSubnets) != 0 || f.createVPCCalls != 0 {
		t.Errorf("mutated with CreateEnabled=false: vpcs=%d subnets=%v", f.createVPCCalls, f.createdSubnets)
	}
}

func TestCreateDisabledWithNoVpcStopsBeforeZoneLookup(t *testing.T) {
	withCreateEnabled(t, false)
	f := &fakeEC2{zones: []string{"us-west-2a"}}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if f.createVPCCalls != 0 {
		t.Errorf("created a VPC with CreateEnabled=false")
	}
	if f.azFilters != nil {
		t.Errorf("looked up zones with no VPC to place subnets in")
	}
}

func TestDescribeVpcsErrorIsReturned(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{vpcsErr: errors.New("throttled")}
	err := reconcile(context.Background(), f)
	if err == nil || !strings.Contains(err.Error(), "throttled") {
		t.Fatalf("err = %v, want it to wrap the underlying failure", err)
	}
}

func TestSubnetPagingErrorIsReturned(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{
		vpcs:       []ec2types.Vpc{{VpcId: aws.String("vpc-1")}},
		zones:      []string{"us-west-2a"},
		subnetsErr: errors.New("access denied"),
	}
	err := reconcile(context.Background(), f)
	if err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Fatalf("err = %v, want the subnet lookup failure", err)
	}
	if len(f.createdSubnets) != 0 {
		t.Errorf("created subnets despite not knowing existing coverage")
	}
}

func TestCreateDefaultVpcWithoutIdIsAnError(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{createVPCNil: true, zones: []string{"us-west-2a"}}
	err := reconcile(context.Background(), f)
	if err == nil {
		t.Fatal("nil error when the created VPC carried no id")
	}
	if len(f.createdSubnets) != 0 {
		t.Errorf("created subnets against an unknown VPC id")
	}
}

func TestZoneLookupExcludesUnusableZones(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-1")}}}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// CreateDefaultSubnet rejects local and Wavelength zones, and an opted-out
	// zone cannot hold one at all; both must be filtered server-side.
	if !hasFilter(f.azFilters, "zone-type", "availability-zone") {
		t.Errorf("az filters %v do not exclude local/Wavelength zones", f.azFilters)
	}
	if !hasFilter(f.azFilters, "opt-in-status", "opted-in") {
		t.Errorf("az filters %v do not constrain opt-in status", f.azFilters)
	}
}

func TestDefaultVpcLookupIsFilteredServerSide(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-1")}}}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	// Without is-default the first VPC in the account would be adopted as the
	// default, which is how a non-default VPC gets default subnets attached.
	if !hasFilter(f.vpcFilters, "is-default", "true") {
		t.Errorf("vpc filters = %v, want is-default=true", f.vpcFilters)
	}
	if !hasFilter(f.subnetFilters, "default-for-az", "true") {
		t.Errorf("subnet filters = %v, want default-for-az=true", f.subnetFilters)
	}
}
