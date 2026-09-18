import { memo } from "react";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";

const remarkPlugins = [remarkGfm];
const components: Components = {
  table: ({ node: _node, ...props }) => <div className="markdown-table"><table {...props} /></div>,
  a: ({ node: _node, href, ...props }) => <a {...props} href={href}
    target={href && !href.startsWith("#") ? "_blank" : undefined}
    rel={href && !href.startsWith("#") ? "noopener noreferrer" : undefined} />,
};

export const MarkdownMessage = memo(function MarkdownMessage({ text }: { text: string }) {
  return <div className="markdown-message">
    <ReactMarkdown remarkPlugins={remarkPlugins} components={components}>{text}</ReactMarkdown>
  </div>;
});
