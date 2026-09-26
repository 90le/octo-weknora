package docparser

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSimpleFormatReaderMDXPreservesSourceText(t *testing.T) {
	body := []byte("---\ntitle: Architecture overview\n---\n\n# Service map\n<Note>Octo routes through octo-server.</Note>\n")
	result, err := (&SimpleFormatReader{}).Read(context.Background(), &types.ReadRequest{
		FileName: "concepts/architecture-overview.mdx", FileType: "mdx", FileContent: body,
	})
	require.NoError(t, err)
	require.Equal(t, string(body), result.MarkdownContent)
	require.True(t, IsSimpleFormat("MDX"))
	require.Contains(t, (&builtinEngine{}).FileTypes(false), "mdx")
	require.Contains(t, (&simpleEngine{}).FileTypes(false), "mdx")
}

func TestProductionKnowledgeBaseRulesRouteMDXToSimpleReader(t *testing.T) {
	// The production Octo project KB explicitly routes office formats but has
	// no MDX rule. New MDX files must use the Go text reader even in builds that
	// link Anydoc for docx/xlsx, rather than falling into an unsupported engine.
	rules := types.ChunkingConfig{ParserEngineRules: []types.ParserEngineRule{
		{Engine: BuiltinEngineName, FileTypes: []string{"pdf", "pptx"}},
		{Engine: AnydocEngineName, FileTypes: []string{"docx", "xlsx"}},
	}}
	engine := rules.ResolveParserEngine("mdx")
	require.Empty(t, engine)
	reader, err := NewReader(context.Background(), engine, "mdx", false, ReaderDeps{})
	require.NoError(t, err)
	require.IsType(t, &SimpleFormatReader{}, reader)
	result, err := reader.Read(context.Background(), &types.ReadRequest{
		FileName: "concepts/architecture-overview.mdx", FileType: "mdx",
		FileContent: []byte("# Service map\n<Note>Octo routes through octo-server.</Note>"),
	})
	require.NoError(t, err)
	require.Contains(t, result.MarkdownContent, "Octo routes through octo-server")

	// An explicit builtin MDX rule is also valid. The Go engine catalog and
	// Python DocReader registry both advertise MDX, so this uses the remote
	// MarkdownParser rather than an unavailable Anydoc parser.
	remote := &stubRemote{}
	builtin, err := NewReader(context.Background(), BuiltinEngineName, "mdx", false, ReaderDeps{Remote: remote})
	require.NoError(t, err)
	require.Same(t, remote, builtin)
}
