package embedding

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/panjf2000/ants/v2"
	"github.com/stretchr/testify/require"
)

type failingBatchEmbedder struct {
	Embedder
	calls atomic.Int32
}

func (f *failingBatchEmbedder) BatchEmbed(_ context.Context, texts []string) ([][]float32, error) {
	if f.calls.Add(1) == 1 {
		return nil, errors.New("synthetic upstream failure")
	}
	time.Sleep(time.Millisecond)
	result := make([][]float32, len(texts))
	for i := range result {
		result[i] = []float32{1}
	}
	return result, nil
}

func TestBatchEmbedReportsConcurrentProviderFailure(t *testing.T) {
	t.Setenv("BATCH_EMBED_SIZE", "1")
	pool, err := ants.NewPool(8)
	require.NoError(t, err)
	defer pool.Release()
	texts := make([]string, 128)
	_, err = NewBatchEmbedder(pool).BatchEmbedWithPool(context.Background(), &failingBatchEmbedder{}, texts)
	require.ErrorContains(t, err, "synthetic upstream failure")
}

func TestBatchEmbedRejectsNonpositiveBatchSize(t *testing.T) {
	t.Setenv("BATCH_EMBED_SIZE", "0")
	pool, err := ants.NewPool(1)
	require.NoError(t, err)
	defer pool.Release()
	_, err = NewBatchEmbedder(pool).BatchEmbedWithPool(
		context.Background(), &failingBatchEmbedder{}, []string{"synthetic"},
	)
	require.ErrorContains(t, err, "BATCH_EMBED_SIZE")
}
