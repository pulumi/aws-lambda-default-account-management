package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
)

type fakeEC2 struct {
	vpcs         []ec2types.Vpc
	vpcPages     [][]ec2types.Vpc
	vpcsErr      error
	createdVPC   string
	createVPCEr  error
	createVPCNil bool
	zones        []string
	zonesErr     error
	subnetPages  [][]ec2types.Subnet
	subnetsErr   error

	describeSubnetCalls int
	describeVpcCalls    int
	badToken            string
	createdSubnets      []string
	failSubnetsIn       map[string]bool
	conflictSubnetsIn   map[string]bool
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
	f.describeVpcCalls++
	if len(f.vpcPages) == 0 {
		return &ec2.DescribeVpcsOutput{Vpcs: f.vpcs}, nil
	}
	if f.describeVpcCalls > len(f.vpcPages)+2 {
		return nil, fmt.Errorf("DescribeVpcs called %d times for %d pages: token not advancing",
			f.describeVpcCalls, len(f.vpcPages))
	}
	page := 0
	if tok := aws.ToString(in.NextToken); tok != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(tok, "page-"))
		if err != nil {
			f.badToken = tok
			return &ec2.DescribeVpcsOutput{}, nil
		}
		page = n
	}
	if page >= len(f.vpcPages) {
		return &ec2.DescribeVpcsOutput{}, nil
	}
	out := &ec2.DescribeVpcsOutput{Vpcs: f.vpcPages[page]}
	if page+1 < len(f.vpcPages) {
		out.NextToken = aws.String(fmt.Sprintf("page-%d", page+1))
	}
	return out, nil
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
	f.describeSubnetCalls++
	if f.describeSubnetCalls > len(f.subnetPages)+2 {
		// A loop that never advances the token would otherwise spin here until
		// the test binary's timeout, which reads as a hang rather than a bug.
		return nil, fmt.Errorf("DescribeSubnets called %d times for %d pages: token not advancing",
			f.describeSubnetCalls, len(f.subnetPages))
	}

	// The page is selected by the caller's token, not by a call counter, so a
	// loop that never assigns out.NextToken re-reads page 0 forever here just
	// as it would against EC2. A counter-driven fake advances on its own and
	// hides exactly that bug.
	page := 0
	if tok := aws.ToString(in.NextToken); tok != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(tok, "page-"))
		if err != nil {
			f.badToken = tok
			return &ec2.DescribeSubnetsOutput{}, nil
		}
		page = n
	}
	if page >= len(f.subnetPages) {
		return &ec2.DescribeSubnetsOutput{}, nil
	}
	out := &ec2.DescribeSubnetsOutput{Subnets: f.subnetPages[page]}
	if page+1 < len(f.subnetPages) {
		out.NextToken = aws.String(fmt.Sprintf("page-%d", page+1))
	}
	return out, nil
}

func (f *fakeEC2) CreateDefaultSubnet(_ context.Context, in *ec2.CreateDefaultSubnetInput, _ ...func(*ec2.Options)) (*ec2.CreateDefaultSubnetOutput, error) {
	az := aws.ToString(in.AvailabilityZone)
	if f.conflictSubnetsIn[az] {
		return nil, &smithy.GenericAPIError{Code: "InvalidSubnet.Conflict", Message: "already exists"}
	}
	if f.failSubnetsIn[az] {
		return nil, errors.New("boom")
	}
	f.createdSubnets = append(f.createdSubnets, az)
	return &ec2.CreateDefaultSubnetOutput{}, nil
}

func subnetIn(az string) ec2types.Subnet {
	return ec2types.Subnet{AvailabilityZone: aws.String(az)}
}

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

func TestCreatingTheVpcEndsTheRun(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{createdVPC: "vpc-new", zones: []string{"us-west-2a", "us-west-2b"}}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if f.createVPCCalls != 1 {
		t.Fatalf("createVPCCalls = %d, want 1", f.createVPCCalls)
	}
	// CreateDefaultVpc creates a default subnet per zone, and DescribeSubnets
	// would not see them yet. Reconciling on would produce one conflict per
	// zone and report the successful run as a failure.
	if f.describeSubnetCalls != 0 {
		t.Errorf("read subnets %d times after creating the VPC", f.describeSubnetCalls)
	}
	if len(f.createdSubnets) != 0 {
		t.Errorf("created subnets %v after creating the VPC", f.createdSubnets)
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

func TestSubnetLookupErrorAbortsBeforeCreating(t *testing.T) {
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
	want := [][2]string{
		{"zone-type", "availability-zone"},
		{"opt-in-status", "opted-in"},
		// Dropping this one excludes every standard zone in every commercial
		// region, and the job would silently reconcile nothing.
		{"opt-in-status", "opt-in-not-required"},
		{"state", "available"},
	}
	for _, w := range want {
		if !hasFilter(f.azFilters, w[0], w[1]) {
			t.Errorf("az filters %v are missing %s=%s", f.azFilters, w[0], w[1])
		}
	}
}

func TestDefaultVpcLookupIsFilteredServerSide(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-1")}}}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !hasFilter(f.vpcFilters, "is-default", "true") {
		t.Errorf("vpc filters = %v, want is-default=true", f.vpcFilters)
	}
	if !hasFilter(f.subnetFilters, "default-for-az", "true") {
		t.Errorf("subnet filters = %v, want default-for-az=true", f.subnetFilters)
	}
}

func TestZoneLookupErrorIsReturned(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{
		vpcs:     []ec2types.Vpc{{VpcId: aws.String("vpc-1")}},
		zonesErr: errors.New("zone lookup exploded"),
	}
	err := reconcile(context.Background(), f)
	if err == nil || !strings.Contains(err.Error(), "zone lookup exploded") {
		t.Fatalf("err = %v, want the zone lookup failure", err)
	}
	if len(f.createdSubnets) != 0 {
		t.Errorf("created subnets without knowing the zone list")
	}
}

func TestVpcCreationErrorIsReturned(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{createVPCEr: errors.New("quota exceeded"), zones: []string{"us-west-2a"}}
	err := reconcile(context.Background(), f)
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") {
		t.Fatalf("err = %v, want the VPC creation failure", err)
	}
	if f.describeSubnetCalls != 0 || len(f.createdSubnets) != 0 {
		t.Errorf("continued past a failed VPC creation")
	}
}

func TestExistingSubnetConflictIsNotAFailure(t *testing.T) {
	withCreateEnabled(t, true)
	// A concurrent run, or a retry after a call that in fact succeeded, races
	// us to the same zone. The zone ends up correct either way.
	f := &fakeEC2{
		vpcs:              []ec2types.Vpc{{VpcId: aws.String("vpc-1")}},
		zones:             []string{"us-west-2a", "us-west-2b"},
		conflictSubnetsIn: map[string]bool{"us-west-2a": true},
	}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("a conflict on an existing default subnet was reported as failure: %v", err)
	}
}

func TestFailureErrorNamesTheZoneAndTheCause(t *testing.T) {
	withCreateEnabled(t, true)
	f := &fakeEC2{
		vpcs:          []ec2types.Vpc{{VpcId: aws.String("vpc-1")}},
		zones:         []string{"us-west-2a", "us-west-2b"},
		failSubnetsIn: map[string]bool{"us-west-2b": true},
	}
	err := reconcile(context.Background(), f)
	if err == nil {
		t.Fatal("nil error despite a failed creation")
	}
	// Without the cause, a permanent AccessDenied is indistinguishable from a
	// routine capacity refusal in CloudWatch.
	if !strings.Contains(err.Error(), "us-west-2b") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q names neither the zone nor the underlying cause", err)
	}
	// The denominator counts attempts, not every zone in the region.
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("error %q does not report failures over attempts", err)
	}
}

func TestPagingFollowsTheServersToken(t *testing.T) {
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
	if f.badToken != "" {
		t.Errorf("sent an unrecognised page token %q", f.badToken)
	}
	if f.describeSubnetCalls != 2 {
		t.Errorf("describeSubnetCalls = %d, want 2", f.describeSubnetCalls)
	}
}

func TestDefaultVpcFoundOnALaterPage(t *testing.T) {
	withCreateEnabled(t, true)
	// EC2 can return a page that matches nothing but carries a token. Treating
	// the first empty page as "no default VPC" would create a second one.
	f := &fakeEC2{
		vpcPages: [][]ec2types.Vpc{
			{},
			{{VpcId: aws.String("vpc-late")}},
		},
		zones: []string{"us-west-2a"},
		subnetPages: [][]ec2types.Subnet{
			{subnetIn("us-west-2a")},
		},
	}
	if err := reconcile(context.Background(), f); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if f.createVPCCalls != 0 {
		t.Errorf("created a VPC despite one existing on page 2")
	}
	if f.badToken != "" {
		t.Errorf("sent an unrecognised page token %q", f.badToken)
	}
	if !hasFilter(f.subnetFilters, "vpc-id", "vpc-late") {
		t.Errorf("subnet lookup filters = %v, want vpc-id=vpc-late", f.subnetFilters)
	}
}
