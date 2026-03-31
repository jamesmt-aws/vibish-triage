package parser

import "testing"

const samplePrompt = `# Extract Issues

Do the thing.

## Infrastructure Setup

` + "```bash" + `
test -s issues.jsonl
echo "ready"
` + "```" + `

## Strategy

Process in batches.

## Validation

` + "```bash" + `
test -s output.jsonl
python3 -c "print('ok')"
` + "```" + `

## Tools

- python3
- gh
`

func TestParse(t *testing.T) {
	p := Parse(samplePrompt)

	if p.Body != samplePrompt {
		t.Error("Body should be the full markdown")
	}

	if p.InfraSetup != "test -s issues.jsonl\necho \"ready\"" {
		t.Errorf("InfraSetup = %q", p.InfraSetup)
	}

	if p.Validation != "test -s output.jsonl\npython3 -c \"print('ok')\"" {
		t.Errorf("Validation = %q", p.Validation)
	}

	if len(p.Tools) != 2 || p.Tools[0] != "python3" || p.Tools[1] != "gh" {
		t.Errorf("Tools = %v", p.Tools)
	}
}

func TestParseEmpty(t *testing.T) {
	p := Parse("# Just a title\n\nSome text.")
	if p.InfraSetup != "" || p.Validation != "" || len(p.Tools) != 0 {
		t.Error("should have no extracted sections")
	}
}

func TestParseIgnoresCodeFenceHeadings(t *testing.T) {
	md := "# Title\n\n```\n## Not a Heading\n```\n\n## Tools\n\n- git\n"
	p := Parse(md)
	if len(p.Tools) != 1 || p.Tools[0] != "git" {
		t.Errorf("Tools = %v", p.Tools)
	}
}
