// Package dynamorepo implements the ports.JobRepository port on Amazon
// DynamoDB.
//
// Job state lives in one table keyed by the job identifier. Status transitions
// are conditional writes, so an update against a job that no longer exists
// fails rather than silently recreating it.
package dynamorepo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"imageforge/internal/domain"
	"imageforge/internal/ports"
)

// Compile-time assertion that JobRepository satisfies the port.
var _ ports.JobRepository = (*JobRepository)(nil)

// KeyAttribute is the table's partition key, which
// deployments/docker/localstack-init.sh creates the table with.
const KeyAttribute = "id"

// JobRepository stores jobs in a DynamoDB table.
//
// It is safe for concurrent use.
type JobRepository struct {
	client *dynamodb.Client
	table  string
	now    func() time.Time
}

// Option overrides a JobRepository setting.
type Option func(*JobRepository)

// WithClock replaces the clock used to stamp UpdatedAt, for deterministic
// tests.
func WithClock(now func() time.Time) Option {
	return func(r *JobRepository) {
		if now != nil {
			r.now = now
		}
	}
}

// New wires a repository to a table.
func New(client *dynamodb.Client, table string, opts ...Option) (*JobRepository, error) {
	if client == nil {
		return nil, errors.New("dynamorepo: nil client")
	}
	if strings.TrimSpace(table) == "" {
		return nil, errors.New("dynamorepo: table name is empty")
	}

	repo := &JobRepository{client: client, table: table, now: time.Now}
	for _, opt := range opts {
		opt(repo)
	}
	return repo, nil
}

// Table returns the table jobs are stored in.
func (r *JobRepository) Table() string { return r.table }

// item is the stored shape of a job.
//
// It is kept separate from domain.Job so the storage format is an explicit
// decision rather than a side effect of the domain's field names, and so
// renaming a domain field does not silently orphan every stored record.
type item struct {
	ID             string `dynamodbav:"id"`
	OriginalKey    string `dynamodbav:"original_key"`
	ResultKey      string `dynamodbav:"result_key"`
	Status         string `dynamodbav:"status"`
	Width          int    `dynamodbav:"width"`
	Height         int    `dynamodbav:"height"`
	Format         string `dynamodbav:"format"`
	Quality        int    `dynamodbav:"quality"`
	Watermark      bool   `dynamodbav:"watermark"`
	StripMetadata  bool   `dynamodbav:"strip_metadata"`
	CreatedAtUnix  int64  `dynamodbav:"created_at"`
	UpdatedAtUnix  int64  `dynamodbav:"updated_at"`
	ProcessedError string `dynamodbav:"error"`
}

// newItem maps a job onto its stored shape.
//
// Timestamps are stored as Unix nanoseconds: a number sorts and compares in
// DynamoDB without the ambiguity a formatted string would carry.
func newItem(job *domain.Job) item {
	return item{
		ID:             job.ID,
		OriginalKey:    job.OriginalKey,
		ResultKey:      job.ResultKey,
		Status:         job.Status.String(),
		Width:          job.Transformation.Width,
		Height:         job.Transformation.Height,
		Format:         job.Transformation.Format.String(),
		Quality:        job.Transformation.Quality,
		Watermark:      job.Transformation.Watermark,
		StripMetadata:  job.Transformation.StripMetadata,
		CreatedAtUnix:  job.CreatedAt.UnixNano(),
		UpdatedAtUnix:  job.UpdatedAt.UnixNano(),
		ProcessedError: job.Error,
	}
}

// toDomain maps a stored record back onto a job.
func (i item) toDomain() *domain.Job {
	return &domain.Job{
		ID:          i.ID,
		OriginalKey: i.OriginalKey,
		ResultKey:   i.ResultKey,
		Status:      domain.JobStatus(i.Status),
		Transformation: domain.TransformationSpec{
			Width:         i.Width,
			Height:        i.Height,
			Format:        domain.Format(i.Format),
			Quality:       i.Quality,
			Watermark:     i.Watermark,
			StripMetadata: i.StripMetadata,
		},
		CreatedAt: time.Unix(0, i.CreatedAtUnix).UTC(),
		UpdatedAt: time.Unix(0, i.UpdatedAtUnix).UTC(),
		Error:     i.ProcessedError,
	}
}

// Save stores job, creating it or replacing it wholesale.
func (r *JobRepository) Save(ctx context.Context, job *domain.Job) error {
	if job == nil {
		return errors.New("dynamorepo: save: nil job")
	}
	if job.ID == "" {
		return errors.New("dynamorepo: save: empty job id")
	}

	attrs, err := attributevalue.MarshalMap(newItem(job))
	if err != nil {
		return fmt.Errorf("dynamorepo: save %s: marshal: %w", job.ID, err)
	}

	if _, err = r.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(r.table),
		Item:      attrs,
	}); err != nil {
		return fmt.Errorf("dynamorepo: save %s: %w", job.ID, err)
	}
	return nil
}

// Get loads the job with the given id, returning ports.ErrJobNotFound when it
// does not exist.
//
// The read is strongly consistent: a client polling for a job it has just
// created must not be told it does not exist.
func (r *JobRepository) Get(ctx context.Context, id string) (*domain.Job, error) {
	if id == "" {
		return nil, fmt.Errorf("dynamorepo: get: %w", ports.ErrJobNotFound)
	}

	out, err := r.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(r.table),
		Key:            key(id),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("dynamorepo: get %s: %w", id, err)
	}
	if len(out.Item) == 0 {
		return nil, fmt.Errorf("dynamorepo: get %s: %w", id, ports.ErrJobNotFound)
	}

	var stored item
	if err = attributevalue.UnmarshalMap(out.Item, &stored); err != nil {
		return nil, fmt.Errorf("dynamorepo: get %s: unmarshal: %w", id, err)
	}
	return stored.toDomain(), nil
}

// UpdateStatus transitions the job to status.
//
// resultKey is recorded when non-empty, and procErr as the failure reason when
// non-nil; a nil procErr clears any previous one. The write is conditional on
// the job existing, so a status update can never resurrect a deleted job as a
// partial record.
func (r *JobRepository) UpdateStatus(
	ctx context.Context,
	id string,
	status domain.JobStatus,
	resultKey string,
	procErr error,
) error {
	if id == "" {
		return fmt.Errorf("dynamorepo: update: %w", ports.ErrJobNotFound)
	}
	if !status.Valid() {
		return fmt.Errorf("dynamorepo: update %s: invalid status %q", id, status)
	}

	set := []string{"#status = :status", "#updated_at = :updated_at", "#error = :error"}
	names := map[string]string{
		"#status":     "status",
		"#updated_at": "updated_at",
		"#error":      "error",
	}
	values := map[string]types.AttributeValue{
		":status":     &types.AttributeValueMemberS{Value: status.String()},
		":updated_at": &types.AttributeValueMemberN{Value: fmt.Sprint(r.now().UTC().UnixNano())},
		":error":      &types.AttributeValueMemberS{Value: errorText(procErr)},
	}

	if resultKey != "" {
		set = append(set, "#result_key = :result_key")
		names["#result_key"] = "result_key"
		values[":result_key"] = &types.AttributeValueMemberS{Value: resultKey}
	}

	_, err := r.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(r.table),
		Key:                       key(id),
		UpdateExpression:          aws.String("SET " + strings.Join(set, ", ")),
		ConditionExpression:       aws.String("attribute_exists(#key)"),
		ExpressionAttributeNames:  withKeyName(names),
		ExpressionAttributeValues: values,
	})
	if err != nil {
		var conditionFailed *types.ConditionalCheckFailedException
		if errors.As(err, &conditionFailed) {
			return fmt.Errorf("dynamorepo: update %s: %w", id, ports.ErrJobNotFound)
		}
		return fmt.Errorf("dynamorepo: update %s: %w", id, err)
	}
	return nil
}

// key builds the primary key of a job.
func key(id string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		KeyAttribute: &types.AttributeValueMemberS{Value: id},
	}
}

// withKeyName adds the placeholder the existence condition refers to.
func withKeyName(names map[string]string) map[string]string {
	names["#key"] = KeyAttribute
	return names
}

// errorText renders a processing error for storage, empty when there is none.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
