package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestDeletePrefixAllPages(t *testing.T) {
	for _, failSecondPage := range []bool{false, true} {
		t.Run(map[bool]string{false: "all pages", true: "list failure"}[failSecondPage], func(t *testing.T) {
			var deleted []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					deleted = append(deleted, r.URL.Path)
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if r.URL.Query().Get("prefix") != "problem/" {
					t.Error("missing prefix filter")
				}
				w.Header().Set("Content-Type", "application/xml")
				if r.URL.Query().Get("continuation-token") == "next" {
					if failSecondPage {
						w.WriteHeader(http.StatusForbidden)
						io.WriteString(w, `<Error><Code>AccessDenied</Code></Error>`)
						return
					}
					io.WriteString(w, `<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>problem/version2</Key></Contents></ListBucketResult>`)
				} else {
					io.WriteString(w, `<ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>problem/version1</Key></Contents></ListBucketResult>`)
				}
			}))
			defer server.Close()
			client := awss3.NewFromConfig(aws.Config{
				Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
				HTTPClient: server.Client(),
			}, func(o *awss3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
			storage := &S3{client: client, Bucket: "test"}
			err := storage.DeletePrefix(context.Background(), "problem/")
			if (err != nil) != failSecondPage {
				t.Fatalf("delete prefix error: %v", err)
			}
			want := []string{"/test/problem/version1"}
			if !failSecondPage {
				want = append(want, "/test/problem/version2")
			}
			if !reflect.DeepEqual(deleted, want) {
				t.Fatalf("deleted %v; want %v", deleted, want)
			}
		})
	}
}
