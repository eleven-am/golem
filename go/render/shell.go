package render

import (
	"bytes"
	"html"
	"net/url"
	"regexp"
	"strings"
)

type shell struct {
	prefix        []byte
	prefixNoTitle []byte
	suffix        []byte
	original      []byte
}

var titlePattern = regexp.MustCompile(`(?is)<title\b[^>]*>.*?</title>`)

func parseShell(source []byte) (shell, error) {
	lower := bytes.ToLower(source)
	closing := bytes.Index(lower, []byte("</head>"))
	if closing < 0 {
		return shell{}, fail(CodeIndexInvalid, "index contains no closing </head> tag")
	}
	prefix := source[:closing]
	suffix := source[closing:]
	return shell{
		prefix:        prefix,
		prefixNoTitle: titlePattern.ReplaceAll(prefix, nil),
		suffix:        suffix,
		original:      source,
	}, nil
}

type tag struct {
	name  string
	value string
}

func (renderer *Renderer) render(meta *Meta, requestURL string) []byte {
	merged, present := renderer.merge(meta)
	if !present {
		return renderer.shell.original
	}
	tags := renderer.tags(merged, requestURL)
	prefix := renderer.shell.prefix
	var head bytes.Buffer
	if merged.Title != "" {
		prefix = renderer.shell.prefixNoTitle
		head.WriteString("<title>")
		head.WriteString(html.EscapeString(merged.Title))
		head.WriteString("</title>")
	}
	for _, item := range tags {
		attribute := "name"
		if strings.HasPrefix(item.name, "og:") || strings.HasPrefix(item.name, "article:") {
			attribute = "property"
		}
		head.WriteString(`<meta `)
		head.WriteString(attribute)
		head.WriteString(`="`)
		head.WriteString(html.EscapeString(item.name))
		head.WriteString(`" content="`)
		head.WriteString(html.EscapeString(item.value))
		head.WriteString(`">`)
	}
	rendered := make([]byte, 0, len(prefix)+head.Len()+len(renderer.shell.suffix))
	rendered = append(rendered, prefix...)
	rendered = append(rendered, head.Bytes()...)
	rendered = append(rendered, renderer.shell.suffix...)
	return rendered
}

func (renderer *Renderer) merge(meta *Meta) (Meta, bool) {
	merged := renderer.defaults
	empty := renderer.defaults.Title == "" && renderer.defaults.Description == "" &&
		renderer.defaults.Image == "" && renderer.defaults.URL == "" &&
		len(renderer.defaults.Tags) == 0
	if meta == nil {
		return merged, !empty
	}
	if meta.Title != "" {
		merged.Title = meta.Title
	}
	if meta.Description != "" {
		merged.Description = meta.Description
	}
	if meta.Image != "" {
		merged.Image = meta.Image
	}
	if meta.URL != "" {
		merged.URL = meta.URL
	}
	combined := map[string]string{}
	for name, value := range renderer.defaults.Tags {
		combined[name] = value
	}
	for name, value := range meta.Tags {
		combined[name] = value
	}
	merged.Tags = combined
	merged.Omit = meta.Omit
	return merged, true
}

func (renderer *Renderer) tags(meta Meta, requestURL string) []tag {
	generated := map[string]string{}
	order := []string{}
	add := func(name, value string) {
		if value == "" {
			return
		}
		if _, exists := generated[name]; !exists {
			order = append(order, name)
		}
		generated[name] = value
	}

	location := meta.URL
	if location == "" {
		location = requestURL
	}
	location = renderer.absolute(location)
	image := renderer.absolute(meta.Image)

	add("description", meta.Description)
	add("og:type", "website")
	add("og:url", location)
	add("og:title", meta.Title)
	add("og:description", meta.Description)
	add("og:image", image)
	if image != "" {
		add("twitter:card", "summary_large_image")
	}
	add("twitter:title", meta.Title)
	add("twitter:description", meta.Description)
	add("twitter:image", image)

	for name, value := range meta.Tags {
		add(name, value)
	}
	for _, name := range meta.Omit {
		delete(generated, name)
	}

	result := make([]tag, 0, len(generated))
	for _, name := range order {
		value, present := generated[name]
		if !present {
			continue
		}
		result = append(result, tag{name: name, value: value})
	}
	return result
}

func (renderer *Renderer) absolute(value string) string {
	if value == "" || renderer.base == nil {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	return renderer.base.ResolveReference(parsed).String()
}
