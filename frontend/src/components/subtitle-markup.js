import React from "react";

// Safe subtitle inline-markup renderer.
//
// Parses simple HTML-like inline tags found in SRT/ASS/VTT subtitle text
// (i, em, b, strong, u, br) and renders them as React elements. Unknown or
// malformed markup is rendered as harmless plain text — never via
// dangerouslySetInnerHTML. This prevents XSS from any subtitle content while
// preserving common formatting semantics.
//
// Supported tags: <i>, <em>, <b>, <strong>, <u>, <br/>, <br>
// Everything else (unknown tags, entities, stray angle brackets) is treated as
// literal text.

const ALLOWED_TAGS = new Set(["i", "em", "b", "strong", "u"])

// Tokenize the input into a sequence of text and tag tokens.
function tokenize(text) {
  const tokens = []
  // Match either an opening/closing/self-closing tag or a run of non-tag text.
  const regex = /<\/?([a-zA-Z][a-zA-Z0-9]*)\s*\/?>|[^<]+|</g
  let match
  let lastIndex = 0
  while ((match = regex.exec(text)) !== null) {
    if (match[0] === "<") {
      // Stray "<" — treat as literal text.
      tokens.push({type: "text", value: "<"})
      lastIndex = regex.lastIndex
    } else if (match[1] !== undefined) {
      // A tag. Determine if it's closing, self-closing, or opening.
      const raw = match[0]
      const tagName = match[1].toLowerCase()
      const isClosing = raw.startsWith("</")
      const isSelfClosing = raw.endsWith("/>")
      tokens.push({type: "tag", tagName, isClosing, isSelfClosing, raw})
      lastIndex = regex.lastIndex
    } else {
      // Plain text run.
      tokens.push({type: "text", value: match[0]})
      lastIndex = regex.lastIndex
    }
  }
  // Capture any trailing text after the last match (e.g. trailing "<").
  if (lastIndex < text.length) {
    tokens.push({type: "text", value: text.slice(lastIndex)})
  }
  return tokens
}

// Build React elements from tokens, using a stack for nesting.
export function renderSubtitleMarkup(text) {
  if (!text) return text
  const tokens = tokenize(text)
  const root = {children: []}
  const stack = [root]

  for (const token of tokens) {
    const current = stack[stack.length - 1]

    if (token.type === "text") {
      current.children.push(token.value)
      continue
    }

    // It's a tag.
    const {tagName, isClosing, isSelfClosing, raw} = token

    // <br> or <br/> → line break.
    if (tagName === "br") {
      current.children.push(React.createElement("br"))
      continue
    }

    if (!ALLOWED_TAGS.has(tagName)) {
      // Unknown tag — render its raw source as literal text.
      current.children.push(raw)
      continue
    }

    if (isClosing) {
      // Pop the stack if there's a matching open tag.
      // Search down the stack for a matching open tag.
      for (let i = stack.length - 1; i >= 1; i--) {
        if (stack[i].tag === tagName) {
          // Collapse everything above back into the matching tag's children as text.
          // Simpler: just pop to the matching tag.
          stack.length = i
          break
        }
      }
      // If no match found, the closing tag is stray — ignore it.
      continue
    }

    // Opening or self-closing allowed tag.
    const element = {tag: tagName, children: [], key: current.children.length}
    current.children.push(element)

    if (!isSelfClosing) {
      stack.push(element)
    }
  }

  // Any unclosed tags remain on the stack — their children are already attached
  // to the tree, so we just don't pop. The tree is complete.

  return toReact(root)
}

// Recursively convert our node tree into React elements.
function toReact(node) {
  if (node.children.length === 1 && typeof node.children[0] === "string") {
    if (node.tag) {
      return React.createElement(node.tag, {key: node.key}, node.children[0])
    }
    return node.children[0]
  }
  const children = node.children.map((child, i) => {
    if (typeof child === "string") return child
    if (React.isValidElement(child)) return child
    return toReact({...child, key: i})
  })
  if (node.tag) {
    return React.createElement(node.tag, {key: node.key}, children)
  }
  // Root node — return a fragment if multiple children, or the single child.
  if (children.length === 1) return children[0]
  return React.createElement(React.Fragment, null, children)
}

// Strip markup for plain-text use (search, aria-labels).
export function stripSubtitleMarkup(text) {
  if (!text) return text
  return String(text)
    .replace(/<br\s*\/?>/gi, " ")
    .replace(/<\/?([a-zA-Z][a-zA-Z0-9]*)\s*\/?>/g, "")
    .replace(/</g, "<")
    .replace(/\s+/g, " ")
    .trim()
}