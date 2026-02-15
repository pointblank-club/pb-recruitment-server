package s3

import (
	"app/internal/common"
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/labstack/gommon/log"
)

type S3 struct {
	client *s3.Client
	Bucket string
}

func NewS3Client() *S3 {
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	client := s3.NewFromConfig(cfg)

	return &S3{
		client: client,
		Bucket: os.Getenv("S3_SUBMISSIONS_BUCKET"),
	}
}

func (s *S3) PutObject(context context.Context, key string, contents string) error {
	_, err := s.client.PutObject(context, &s3.PutObjectInput{
		Bucket:      aws.String(s.Bucket),
		Key:         aws.String(key),
		Body:        strings.NewReader(contents),
		IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
			log.Errorf("s3: key %s already exists: %v", key, err)
			return common.KeyAlreadyExistsError
		}
		log.Errorf("s3: failed to upload object: %v", err)
	}
	return err
}

func (s *S3) GetObject(context context.Context, key string) (string, error) {
	resp, err := s.client.GetObject(context, &s3.GetObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var notFound *types.NoSuchKey
		if errors.As(err, &notFound) {
			log.Errorf("s3: key %s not found: %v", key, err)
			return "", common.KeyNotFoundError
		}
		log.Errorf("s3: failed to get object: %v", err)
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("s3: failed to read object body: %v", err)
		return "", err
	}

	return string(body), nil
}

func (s *S3) PutObjectOverwrite(ctx context.Context, key string, content string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(key),
		Body:   strings.NewReader(content),
	})
	return err
}

func (s *S3) DeleteObject(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		log.Errorf("s3: failed to delete object %s: %v", key, err)
		return err
	}
	return nil
}

func (s *S3) ListObjects(ctx context.Context, prefix string) ([]string, error) {
	var keys []string

	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.Bucket),
		Prefix: aws.String(prefix),
	})

	for paginator.HasMorePages() {
		out, err := paginator.NextPage(ctx)
		if err != nil {
			log.Errorf("s3: list objects failed: %v", err)
			return nil, err
		}
		for _, obj := range out.Contents {
			if obj.Key != nil {
				keys = append(keys, *obj.Key)
			}
		}
	}
	return keys, nil
}

func (s *S3) DeletePrefix(ctx context.Context, prefix string) error {
	out, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.Bucket),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return err
	}

	for _, obj := range out.Contents {
		_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.Bucket),
			Key:    obj.Key,
		})
		if err != nil {
			return err
		}
	}

	return nil
}
