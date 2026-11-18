/**
 * HTML preview isolation (design: HTML dual-mode preview).
 *
 * The previewed document is rendered in a sandboxed iframe WITHOUT
 * `allow-same-origin`, so it gets an opaque origin: it cannot read our
 * cookies/JWT, cannot touch the parent DOM, and cannot reach the app's
 * storage. `<base href="about:srcdoc">` makes relative URLs resolve
 * against the document itself instead of the app origin, so an untrusted
 * file cannot turn the preview into a request for our API with the user's
 * session.
 *
 * In-page anchors (#section) are handled inside the frame; other links are
 * left inert (no allow-top-navigation / allow-popups-to-escape-sandbox).
 */

const GUARD_MARKER = "data-picoclaw-preview-guard"

/** Keeps in-page anchors working inside the sandboxed document. */
const GUARD = `<base href="about:srcdoc"><script ${GUARD_MARKER}>document.addEventListener("click",function(e){var n=e.target;while(n&&n.tagName!=="A")n=n.parentElement;if(!n)return;var h=n.getAttribute("href")||"";if(h.charAt(0)!=="#")return;e.preventDefault();var id=h.slice(1);var el=id?(document.getElementById(id)||document.getElementsByName(id)[0]):null;if(el&&el.scrollIntoView)el.scrollIntoView();});</script>`

/** Prepare raw HTML for safe rendering inside the sandboxed frame. */
export function isolateHtmlPreview(content: string): string {
  if (content.includes(GUARD_MARKER)) return content

  const head = content.match(/<head[^>]*>/i)
  if (head && head.index != null) {
    const at = head.index + head[0].length
    return content.slice(0, at) + GUARD + content.slice(at)
  }

  const html = content.match(/<html[^>]*>/i)
  if (html && html.index != null) {
    const at = html.index + html[0].length
    return content.slice(0, at) + `<head>${GUARD}</head>` + content.slice(at)
  }

  return `<!DOCTYPE html><html><head>${GUARD}</head><body>${content}</body></html>`
}

/** Sandbox flags: scripts run in the opaque origin, nothing escapes it. */
export const HTML_PREVIEW_SANDBOX = "allow-scripts allow-popups allow-forms"
