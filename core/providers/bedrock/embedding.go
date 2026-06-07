package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/maximhq/bifrost/core/types"
)

type TitanEmbeddingRequest struct {
	InputText string `json:"inputText"`
}

type TitanEmbeddingResponse struct {
	Embedding           []float32 `json:"embedding"`
	InputTextTokenCount int       `json:"inputTextTokenCount"`
}

// BedrockProvider 适配器结构体
type BedrockProvider struct {
	client *bedrockruntime.Client
}

// CreateEmbedding 核心修复函数：完美支持 OpenAI 规范的批量 Input 数组输入
func (p *BedrockProvider) CreateEmbedding(ctx context.Context, request *types.EmbeddingRequest) (*types.EmbeddingResponse, error) {
	numInputs := len(request.Input)
	dataResults := make([]types.EmbeddingData, numInputs)
	errChan := make(chan error, numInputs)
	
	var wg sync.WaitGroup
	var totalTokens int
	var mu sync.Mutex

	// 分发多协程同时并发请求 AWS，解决 400 报错与静默合并 Bug
	for i, inputText := range request.Input {
		wg.Add(1)
		go func(index int, text string) {
			defer wg.Done()
			var payload []byte
			var err error
			
			if strings.HasPrefix(request.Model, "amazon.titan") {
				payload, err = json.Marshal(TitanEmbeddingRequest{InputText: text})
			} else {
				errChan <- fmt.Errorf("unsupported embedding model: %s", request.Model)
				return
			}
			if err != nil {
				errChan <- err
				return
			}
			
			output, err := p.client.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
				ModelId:     aws.String(request.Model),
				ContentType: aws.String("application/json"),
				Accept:      aws.String("application/json"),
				Body:        payload,
			})
			if err != nil {
				errChan <- err
				return
			}
			
			var titanResp TitanEmbeddingResponse
			if err := json.Unmarshal(output.Body, &titanResp); err != nil {
				errChan <- err
				return
			}
			
			// 严格加锁，保证多线程回填的 Index 顺序与用户请求完全一致
			mu.Lock()
			totalTokens += titanResp.InputTextTokenCount
			dataResults[index] = types.EmbeddingData{
				Object:    "embedding",
				Embedding: titanResp.Embedding,
				Index:     index,
			}
			mu.Unlock()
		}(i, inputText)
	}
	
	wg.Wait()
	
	select {
	case err := <-errChan:
		return nil, err
	default:
	}
	
	return &types.EmbeddingResponse{
		Object: "list",
		Data:   dataResults,
		Model:  request.Model,
		Usage: types.Usage{
			PromptTokens: totalTokens,
			TotalTokens:  totalTokens,
		},
	}, nil
}
