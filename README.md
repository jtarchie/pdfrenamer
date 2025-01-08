# PDF Renamer

This will use an LLM that supports the OpenAI HTTP endpoint.
It will extract information from a PDF file and rename the file accordingly.
The `--format` option can be used with [sprig](http://masterminds.github.io/sprig/) template functions to do transformations on the templates values.

## Usage

This is assuming that [ollama](https://ollama.com/) is being used.

```bash
brew bundle

# in one tab
ollama start

# in another tab
ollama pull llama3.2-vision
ollama pull llama3.2
go run main.go \
  --endpoint http://localhost:11434/v1/ \
  --image-model "llama3.2-vision" \
  --text-model "llama3.2" \
  --format "{{.Date | snakecase}}-{{.Company | snakecase}}-{{.AccountNumber | snakecase}}.pdf" \
  --prompt "Please convert dates to YYYY-MM-DD where applicable." \
  --dry-run \
  <pdf file>
```