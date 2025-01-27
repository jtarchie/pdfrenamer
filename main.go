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
	openAIClient := openai.NewClientWithConfig(config)

	chunks := []string{}

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

		const promptPDFtoMarkdown = `
You are tasked with converting an image of a page from a PDF document into a markdown text representation. Follow these strict guidelines to ensure accuracy and consistency:
1. Include **all visible content from the page** without omitting or altering any information for privacy or any other reasons. 
2. **Preserve the original structure** and intent of the document:
   - Convert headings to appropriate markdown heading levels ('#', '##', etc.), ensuring a blank line before and after each heading.
   - Keep paragraphs intact, ensuring no line breaks occur within words (e.g., "cor- rect" becomes "correct").
   - Reformat lists into proper markdown syntax:
     - Unordered lists: '-' or '*'
     - Ordered lists: '1.', '2.', etc.
3. Apply markdown formatting to enhance readability:
   - Use '*italic*' and '**bold**' where present in the original content.
   - Convert tables into markdown table format. Retain all rows and columns as they appear.
4. Identify and **clearly mark headers, footers, and page numbers** as blockquotes ('>') but do not remove them.
5. Strictly preserve original punctuation and capitalization:
   - Do not add punctuation or modify the existing punctuation.
   - Maintain original text flow without introducing unnecessary explanations.
6. Handle duplicate content carefully:
   - Remove only **exact or near-exact duplicates** within the page.
   - Cross-check the context (before and after the main chunk) to avoid accidental removal of meaningful content.
   - If no duplicates are identified, return the content as is.
7. Avoid injecting additional content:
   - Do not add introductory text like "Here is the converted text" or similar phrases.
   - Ensure the output contains only the content extracted from the image.
`

		response, err := openAIClient.CreateChatCompletion(
			context.Background(),
			openai.ChatCompletionRequest{
				Model: c.ImageModel,
				Messages: []openai.ChatCompletionMessage{
					{
						Role:    "system",
						Content: promptPDFtoMarkdown,
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

		chunks = append(chunks, response.Choices[0].Message.Content)
	}

	markdown := strings.Join(chunks, "\n\n")
	slog.Info("extract", "prompt", c.Prompt, "format", c.Format, "markdown", markdown)

	// for all markdown use OpenAI text model to extract
	response, err := openAIClient.CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model: c.TextModel,
			Messages: []openai.ChatCompletionMessage{
				{
					Role: "system",
					Content: fmt.Sprintf(`
You are provided with a markdown document, and your task is to extract specific information to generate a JSON object. The extracted information will be used to construct a filename using a Go 'text/template' format. Follow these instructions precisely:
1. **Understand the provided context:**
	- The user has requested specific guidance for extraction: '%s'.   
	- The filename format is: '%s'.
2. Extract the required fields from the markdown document:
   - Each field corresponds to a key in the filename template (e.g., '{{.Title}}').
   - Ensure that the extracted fields strictly match the case of the keys in the template.
3. Output the extracted data as a valid JSON object:
   - Use string key-value pairs only.
   - For example, if the format is '{{.Title | snakecase}}', output should be: '{"Title": "My Title"}'.
4. Do not include any extraneous explanation, commentary, or additional data outside the JSON object.
5. Handle potential variations in the markdown document:
   - If a field is missing or ambiguous, make a **best effort** to infer it based on the surrounding context.
   - If inference is not possible, exclude the field from the output.
6. Validate the JSON structure before returning it:
   - Ensure the output is properly formatted and parsable.
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
