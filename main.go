package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/jpeg"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/alecthomas/kong"
	"github.com/gen2brain/go-fitz"
	"github.com/sashabaranov/go-openai"
)

type CLI struct {
	Filename  string `arg:"" type:"existingfile" help:"PDF file to rename"`
	PageRange string `help:"range of pages to analyze from PDF" default:"1"`

	Endpoint string `help:"OpenAI endpoint"`
	ApiKey   string `help:"OpenAI API key"`

	ImageModel string `help:"OpenAI image model" default:"gpt-4o-mini" required:""`
	TextModel  string `help:"OpenAI text model" default:"gpt-4o-mini" required:""`

	Format string `help:"format of the file to rename to" default:"{{.Title}}.pdf"`
	Prompt string `help:"additional info prompt to use to extract text from PDF" default:""`

	DryRun bool `help:"do not rename files, just print what would be done"`
}

func (c *CLI) Run() error {
	startPage, endPage := 0, 0
	pageRange := strings.Split(c.PageRange, "-")
	if len(pageRange) == 1 {
		startPage = 0
		endPage = 0
	} else if len(pageRange) == 2 {
		startPage, _ = strconv.Atoi(pageRange[0])
		endPage, _ = strconv.Atoi(pageRange[0])
	}

	doc, err := fitz.New(c.Filename)
	if err != nil {
		return fmt.Errorf("failed to open PDF: %w", err)
	}
	defer doc.Close()

	config := openai.DefaultConfig(c.ApiKey)
	config.BaseURL = c.Endpoint
	client := openai.NewClientWithConfig(config)

	contents := []string{}

	slog.Info("pdf.process", "start", startPage, "end", endPage)

	// for each page of the PDF convert to image
	for n := 0; n < doc.NumPage(); n++ {
		if n < startPage {
			slog.Info("pdf.skip", "page", n)
			continue
		}
		if endPage < n {
			slog.Info("pdf.end", "page", n)
			break
		}

		slog.Info("pdf.open", "page", n)

		image, err := doc.Image(n)
		if err != nil {
			return fmt.Errorf("failed to convert page #%d to image: %w", n, err)
		}

		slog.Info("pdf.image", "page", n)

		file := &bytes.Buffer{}
		err = jpeg.Encode(file, image, &jpeg.Options{Quality: 100})
		if err != nil {
			return fmt.Errorf("failed to encode image #%d: %w", n, err)
		}

		slog.Info("pdf.markdown", "page", n)

		encodedImage := base64.StdEncoding.EncodeToString(file.Bytes())

		response, err := client.CreateChatCompletion(
			context.Background(),
			openai.ChatCompletionRequest{
				Model: c.ImageModel,
				Messages: []openai.ChatCompletionMessage{
					{
						Role: "system",
						Content: `
							You convert an image of a page from a PDF document to markdown.
							Please only provide markdown, no explanation or extraneous information about the work.
							Please do your best to convert fields and tables. If you cannot, please just list the information.
							If you are unable to convert the image, please respond with 'N/A'.
						`,
					},
					{
						Role: "user",
						MultiContent: []openai.ChatMessagePart{
							{
								Type: "image_url",
								ImageURL: &openai.ChatMessageImageURL{
									URL:    "data:image/jpeg;base64," + encodedImage,
									Detail: openai.ImageURLDetailAuto,
								},
							},
						},
					},
				},
			},
		)
		if err != nil {
			return fmt.Errorf("failed to convert image #%d to markdown: %w", n, err)
		}

		contents = append(contents, response.Choices[0].Message.Content)
	}

	markdown := strings.Join(contents, "\n\n")
	slog.Info("extract", "prompt", c.Prompt, "format", c.Format, "markdown", markdown)

	// for all markdown use OpenAI text model to extract
	response, err := client.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: c.TextModel,
			Messages: []openai.ChatCompletionMessage{
				{
					Role: "system",
					Content: fmt.Sprintf(`
						The following is markdown document that will be used to extract information from.
						The user would like you to ensure the following about the extraction: %s
						The information extracted will be used to evaluate the format of the filename "%s",
						which is in Go text/template format. No extraneous explanation or content is required.
						Please provide JSON output of the elements from the expected filename.
						For example if the format was {{.Title | snakecase}}, please return JSON of "{"Title": "My Title"}"
						Please keep it as string value key-pairs. Ensure the key names are the same case as the template.
					`, c.Prompt, c.Format),
				},
				{
					Role:    "user",
					Content: markdown,
				},
			},
			ResponseFormat: &openai.ChatCompletionResponseFormat{
				Type: openai.ChatCompletionResponseFormatTypeJSONObject,
			},
		},
	)
	if err != nil {
		return fmt.Errorf("failed to extract information from markdown: %w", err)
	}

	payload := response.Choices[0].Message.Content
	slog.Info("extracted", "payload", payload)

	var values map[string]string
	err = json.Unmarshal([]byte(payload), &values)
	if err != nil {
		return fmt.Errorf("failed to unmarshal JSON payload: %w", err)
	}

	template, err := template.New("filename").Funcs(sprig.FuncMap()).Parse(c.Format)
	if err != nil {
		return fmt.Errorf("failed to parse filename format: %w", err)
	}

	filename := &strings.Builder{}
	err = template.Execute(filename, values)
	if err != nil {
		return fmt.Errorf("failed to execute filename format: %w", err)
	}

	if c.DryRun {
		fmt.Println(filename.String())
	} else {
		err = os.Rename(c.Filename, filename.String())
		if err != nil {
			return fmt.Errorf("failed to rename file: %w", err)
		}
	}

	return nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	cli := &CLI{}
	ctx := kong.Parse(cli)
	// Call the Run() method of the selected parsed command.
	err := ctx.Run()
	ctx.FatalIfErrorf(err)
}
