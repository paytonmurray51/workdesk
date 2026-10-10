// Small, safe Markdown renderer for briefings: headings, lists, paragraphs, **bold**,
// *italic*, `code` and [links](https://…). Builds elements directly (no innerHTML).

const INLINE = /(\*\*[^*]+\*\*|\*[^*\s][^*]*\*|`[^`]+`|\[[^\]]+\]\([^)\s]+\))/g;

function inline(text) {
  const out = [];
  let last = 0;
  for (const m of text.matchAll(INLINE)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const t = m[0];
    if (t.startsWith('**')) out.push(<strong>{t.slice(2, -2)}</strong>);
    else if (t.startsWith('`')) out.push(<code>{t.slice(1, -1)}</code>);
    else if (t.startsWith('[')) {
      const [, label, href] = t.match(/^\[([^\]]+)\]\(([^)\s]+)\)$/);
      out.push(/^https?:\/\//i.test(href) ? <a href={href} target="_blank" rel="noopener noreferrer">{label}</a> : label);
    } else out.push(<em>{t.slice(1, -1)}</em>);
    last = m.index + t.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

export function Markdown({ text }) {
  const blocks = [];
  let list = null;
  let para = [];
  const flushPara = () => {
    if (para.length) blocks.push(<p>{inline(para.join(' '))}</p>);
    para = [];
  };
  const flushList = () => {
    if (list) blocks.push(list.ordered ? <ol>{list.items}</ol> : <ul>{list.items}</ul>);
    list = null;
  };

  for (const raw of text.split('\n')) {
    const line = raw.trimEnd();
    const h = line.match(/^(#{1,4})\s+(.*)$/);
    const ul = line.match(/^\s*[-*+]\s+(.*)$/);
    const ol = line.match(/^\s*\d+[.)]\s+(.*)$/);
    if (h) {
      flushPara();
      flushList();
      // Briefing headings sit inside a tile, so start at h3.
      const level = Math.min(h[1].length + 2, 6);
      const Tag = `h${level}`;
      blocks.push(<Tag>{inline(h[2])}</Tag>);
    } else if (ul || ol) {
      flushPara();
      const ordered = !!ol;
      if (!list || list.ordered !== ordered) {
        flushList();
        list = { ordered, items: [] };
      }
      list.items.push(<li>{inline((ul || ol)[1])}</li>);
    } else if (line.trim() === '') {
      flushPara();
      flushList();
    } else {
      flushList();
      para.push(line.trim());
    }
  }
  flushPara();
  flushList();
  return <div class="md">{blocks}</div>;
}
