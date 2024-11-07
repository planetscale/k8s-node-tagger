package main

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// ec2Client is the minimum interface we need from the AWS SDK to manage node tags
type ec2Client interface {
	DescribeTags(ctx context.Context, params *ec2.DescribeTagsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeTagsOutput, error)
	CreateTags(ctx context.Context, params *ec2.CreateTagsInput, optFns ...func(*ec2.Options)) (*ec2.CreateTagsOutput, error)
	DeleteTags(ctx context.Context, params *ec2.DeleteTagsInput, optFns ...func(*ec2.Options)) (*ec2.DeleteTagsOutput, error)
}

// aws-sdk-go v2's ec2.Client implements our ec2Client interface, so we can use it directly
var _ ec2Client = (*ec2.Client)(nil)
