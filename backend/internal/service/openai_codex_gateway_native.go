package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/MACOS-DO/sub4api/internal/pkg/codexgateway"
	infraerrors "github.com/MACOS-DO/sub4api/internal/pkg/errors"
)

func (s *OpenAIGatewayService) fetchCodexGatewayModels(ctx context.Context, account *Account, version, etag string) (*OpenAIModelsResponse, error) {
	if s.codexGateway == nil || !s.codexGateway.Ready() || account.Gateway == nil {
		return nil, codexgateway.Unavailable()
	}
	source, err := resolveCredentialAccount(ctx, s.accountRepo, account)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set("If-None-Match", etag)
	enforceCodexIdentityHeaders(headers)
	identity, err := s.resolveGatewayIdentity(ctx, source, 0, headers, []byte(`{}`))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(version) == "" {
		version = CodexCanonicalClientVersion()
	}
	response, err := s.codexGateway.client.Forward(ctx, account.Gateway.BindingID, http.MethodGet, "/v1/models?client_version="+url.QueryEscape(version), nil, headers, identity, account.OpenAIRequestTimezone(), nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return &OpenAIModelsResponse{NotModified: true, ETag: response.Header.Get("ETag")}, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, infraerrors.New(response.StatusCode, "CODEX_MODELS_FAILED", "Gateway model catalog request failed")
	}
	limit := int64(4 << 20)
	if s.cfg != nil && s.cfg.Gateway.ModelsListReadMaxBytes > 0 {
		limit = s.cfg.Gateway.ModelsListReadMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, errors.New("Gateway model catalog is too large")
	}
	return &OpenAIModelsResponse{Body: body, ETag: response.Header.Get("ETag"), upstreamETag: response.Header.Get("ETag"), upstreamSourceBody: body}, nil
}

// Multipart conversion is performed before the size check. Gateway receives the
// same native image JSON contract for both uploaded files and image URLs.
func codexGatewayImagesBody(parsed *OpenAIImagesRequest, model string) ([]byte, error) {
	body := map[string]any{}
	if !parsed.Multipart && len(parsed.Body) > 0 {
		if err := json.Unmarshal(parsed.Body, &body); err != nil {
			return nil, err
		}
	}
	body["model"] = model
	body["prompt"] = parsed.Prompt
	if parsed.Multipart {
		if parsed.N > 0 {
			body["n"] = parsed.N
		}
		body["stream"] = parsed.Stream
		for key, value := range map[string]string{"size": parsed.Size, "response_format": parsed.ResponseFormat, "quality": parsed.Quality, "background": parsed.Background, "output_format": parsed.OutputFormat, "moderation": parsed.Moderation, "input_fidelity": parsed.InputFidelity, "style": parsed.Style} {
			if value != "" {
				body[key] = value
			}
		}
		if parsed.OutputCompression != nil {
			body["output_compression"] = *parsed.OutputCompression
		}
		if parsed.PartialImages != nil {
			body["partial_images"] = *parsed.PartialImages
		}
	}
	if parsed.Endpoint == openAIImagesEditsEndpoint {
		images := make([]map[string]string, 0, len(parsed.InputImageURLs)+len(parsed.Uploads))
		for _, value := range parsed.InputImageURLs {
			images = append(images, map[string]string{"image_url": value})
		}
		for _, upload := range parsed.Uploads {
			value := upload.ModerationDataURL()
			if value == "" {
				return nil, errors.New("invalid image upload")
			}
			images = append(images, map[string]string{"image_url": value})
		}
		if len(images) == 0 {
			return nil, errors.New("image edit requires an image")
		}
		body["images"] = images
		delete(body, "image")
		delete(body, "image[]")
		if parsed.MaskUpload != nil {
			body["mask"] = map[string]string{"image_url": parsed.MaskUpload.ModerationDataURL()}
		} else if parsed.MaskImageURL != "" {
			body["mask"] = map[string]string{"image_url": parsed.MaskImageURL}
		}
	}
	return json.Marshal(body)
}
