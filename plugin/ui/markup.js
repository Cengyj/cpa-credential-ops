export const escapeHTML = (value) =>
  String(value ?? '').replace(
    /[&<>"']/g,
    (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c],
  );

export const previewLimit = 1024 * 1024;

function previewCandidate(raw, explicit = false) {
  const source = raw.trim();
  if (/^(?:<style\b[^>]*>[\s\S]*?<\/style\s*>\s*)+<svg\b/i.test(source)) return { kind: 'html', source };
  const root = /(?:^|\n)[\t ]*(<!doctype\s+html\b[^>]*>|<html\b[^>]*>|<svg\b[^>]*>)/i.exec(source);
  if (root) {
    const start = root.index + root[0].indexOf('<');
    const kind = /^<svg\b/i.test(root[1]) ? 'svg' : 'html';
    const rest = source.slice(start);
    if (kind === 'svg' && !explicit && !/\b(?:viewBox|width|height|xmlns)\s*=|<(?:g|path|circle|rect|ellipse|line|polyline|polygon|text|defs|symbol|use)\b/i.test(rest)) return null;
    const closing = new RegExp(`</${kind}\\s*>`, 'gi');
    let end = 0;
    for (const match of rest.matchAll(closing)) end = match.index + match[0].length;
    return { kind, source: end ? rest.slice(0, end) : rest };
  }
  const fragment = /^<(?:style|body|head|main|div|section|article|p|table|h[1-6]|svg)\b/i.test(source);
  if (fragment && (explicit || /^<style\b[\s\S]*?<\/style\s*>\s*<svg\b/i.test(source))) return { kind: 'html', source };
  return null;
}

// Extract only standalone markup; surrounding explanations remain in the saved source.
export function previewSource(raw) {
  if (typeof raw !== 'string' || !raw.trim()) return null;
  const fences = /(?:^|\n)[\t ]{0,3}(`{3,}|~{3,})([\w-]*)[\t ]*\r?\n/g;
  const outside = [];
  let cursor = 0, match;
  while ((match = fences.exec(raw))) {
    outside.push(raw.slice(cursor, match.index));
    const rest = raw.slice(fences.lastIndex);
    const closing = new RegExp(`(?:^|\\n)[\\t ]{0,3}${match[1][0]}{${match[1].length},}[\\t ]*(?=\\r?\\n|$)`).exec(rest);
    const content = closing ? rest.slice(0, closing.index) : rest;
    const language = match[2].toLowerCase();
    if (['', 'html', 'svg', 'xml'].includes(language)) {
      const candidate = previewCandidate(content.replace(/^\s*<\?xml\b[\s\S]*?\?>\s*/i, ''), language !== '');
      if (candidate) return candidate;
    }
    cursor = closing ? fences.lastIndex + closing.index + closing[0].length : raw.length;
    fences.lastIndex = cursor;
  }
  outside.push(raw.slice(cursor));
  return previewCandidate(outside.join('\n').replace(/^\s*<\?xml\b[\s\S]*?\?>\s*/i, ''));
}

export function safePreview(raw) {
  if (raw.length > previewLimit) throw new Error('预览超过 1 MiB 上限。');
  const source = previewSource(raw)?.source || raw;
  // Template contents are inert, including resource loading, until sanitized nodes are connected.
  const template = document.createElement('template');
  template.innerHTML = source;
  const parsed = template.content;
  parsed
    .querySelectorAll('script,iframe,frame,frameset,object,embed,base,meta,link,form,template,noscript')
    .forEach((node) => node.remove());
  parsed.querySelectorAll('a,area,map').forEach((node) => node.replaceWith(...node.childNodes));
  parsed.querySelectorAll('animate,set,animateTransform,animateMotion').forEach((node) => {
    if (
      /^(?:href|xlink:href|src|srcset|action|formaction|target)$/i.test(
        node.getAttribute('attributeName') || '',
      )
    )
      node.remove();
  });
  const svgNamespace = 'http://www.w3.org/2000/svg';
  const identifiers = new Map(), reusableShapes = new WeakMap();
  for (const node of parsed.querySelectorAll('[id]')) identifiers.set(node.id, identifiers.has(node.id) ? null : node);
  const localUse = (node, reference) => {
    if (node.namespaceURI !== svgNamespace || node.localName !== 'use' || !/^#[A-Za-z_][\w.:-]{0,127}$/.test(reference)) return false;
    const root = node.ownerSVGElement;
    if (!root) return false;
    const target = identifiers.get(reference.slice(1));
    if (!target || target.namespaceURI !== svgNamespace || target.ownerSVGElement !== root) return false;
    if (!reusableShapes.has(target)) reusableShapes.set(target,
      ['g', 'path', 'circle', 'ellipse', 'rect', 'line', 'polyline', 'polygon', 'symbol', 'defs', 'text'].includes(target.localName) && !target.querySelector('use,foreignObject'));
    return reusableShapes.get(target);
  };
  for (const node of parsed.querySelectorAll('*')) {
    for (const attribute of [...node.attributes]) {
      if (['href', 'xlink:href'].includes(attribute.name.toLowerCase()) && localUse(node, attribute.value)) continue;
      if (
        /^on/i.test(attribute.name) ||
        [
          'src',
          'srcset',
          'href',
          'xlink:href',
          'action',
          'formaction',
          'ping',
          'poster',
          'background',
          'srcdoc',
          'autofocus',
          'contenteditable',
          'tabindex',
        ].includes(attribute.name.toLowerCase())
      )
        node.removeAttribute(attribute.name);
    }
    if (['button', 'input', 'select', 'textarea'].includes(node.localName)) node.setAttribute('disabled', '');
  }
  const rootAttributes = tag => {
    const opening = new RegExp(`<${tag}\\b(?:[^>"']|"[^"]*"|'[^']*')*>`, 'i').exec(source)?.[0];
    const holder = document.createElement('template');
    holder.innerHTML = (opening ? opening.replace(new RegExp(`^<${tag}\\b`, 'i'), '<div') : '<div>') + '</div>';
    const root = holder.content.firstElementChild;
    root.style.setProperty('overflow', tag === 'html' ? 'auto' : 'visible', 'important');
    root.style.setProperty('height', 'auto', 'important');
    root.style.setProperty('max-height', 'none', 'important');
    if (tag === 'body') root.style.setProperty('min-width', 'min-content', 'important');
    return [...root.attributes]
      .filter(attribute => ['id', 'class', 'lang', 'dir', 'style'].includes(attribute.name.toLowerCase()))
      .map(attribute => ` ${attribute.name}="${escapeHTML(attribute.value)}"`).join('');
  };
  const layout = '<style>html{height:auto!important;max-height:none!important;overflow:auto!important}body{height:auto!important;max-height:none!important;min-width:min-content!important;overflow:visible!important}body>svg{display:block}body>svg[viewBox]{max-width:100%;height:auto}html,body{overscroll-behavior:auto}</style>';
  return `<!doctype html><html${rootAttributes('html')}><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'none'; connect-src 'none'; img-src data:; media-src 'none'; style-src 'unsafe-inline'; font-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'"><meta name="referrer" content="no-referrer"><style>body{margin:16px;font-family:system-ui}svg{max-width:100%}</style></head><body${rootAttributes('body')}>${template.innerHTML}${layout}</body></html>`;
}

// Reconcile trusted view templates without replacing stable reading and editing nodes.
export function patchView(target, html) {
  const template = document.createElement('template');
  template.innerHTML = html;
  const key = (node) => node.nodeType === 1 ? node.dataset.key || node.dataset.part ||
    (node.matches('input,select') && node.closest('.sample-review') ? node.localName : node.id) || '' : '';
  function patchChildren(parent, incoming) {
    const existing = [...parent.childNodes];
    const used = new Set();
    let cursor = parent.firstChild;
    const nextNodes = [...incoming.childNodes];
    if (parent.nodeType === 1 && parent.matches('.comparison-grid')) {
      nextNodes.sort((a, b) => {
        const position = (node) => { const index = existing.findIndex((old) => key(old) === key(node)); return index < 0 ? existing.length : index; };
        return position(a) - position(b);
      });
    }
    for (const next of nextNodes) {
      const nextKey = key(next);
      const current = existing.find((node) => !used.has(node) && node.nodeType === next.nodeType &&
        (nextKey ? key(node) === nextKey : !key(node) && node.nodeName === next.nodeName));
      if (!current) { parent.insertBefore(next.cloneNode(true), cursor); continue; }
      used.add(current);
      if (current !== cursor) parent.insertBefore(current, cursor);
      cursor = current.nextSibling;
      if (current.nodeType === 3) {
        if (current.nodeValue !== next.nodeValue) current.nodeValue = next.nodeValue;
        continue;
      }
      if (current.nodeType !== 1) continue;
      const field = current.matches('input,select,textarea');
      const fieldValue = field ? current.value : null;
      const preservedClasses = ['expanded', ...(current.matches('.model-result,.sample') ? ['hidden'] : [])].filter((name) => current.classList.contains(name));
      for (const attribute of [...current.attributes]) {
        if (['open', 'style', 'aria-busy'].includes(attribute.name)) continue;
        if (attribute.name === 'srcdoc' && current.matches('iframe[data-inline-preview]')) continue;
        if (!next.hasAttribute(attribute.name)) current.removeAttribute(attribute.name);
      }
      for (const attribute of [...next.attributes]) {
        if (attribute.name === 'open') continue;
        if (current.getAttribute(attribute.name) !== attribute.value) current.setAttribute(attribute.name, attribute.value);
      }
      current.classList.add(...preservedClasses);
      const expandedButton = current.matches('[data-expand-sample]') && current.closest('.sample')?.querySelector('.answer-text.expanded');
      if (expandedButton) current.setAttribute('aria-expanded', 'true');
      else patchChildren(current, next);
      if (field) current.value = fieldValue;
    }
    existing.filter((node) => !used.has(node)).forEach((node) => node.remove());
  }
  patchChildren(target, template.content);
}

const inlinePreviewDocuments = new WeakMap();
export function hydrateInlinePreviews(container, run) {
  for (const frame of container.querySelectorAll('iframe[data-inline-preview]')) {
    const sample = run.samples?.[Number(frame.dataset.inlinePreview) - (run.pruned_samples || 0)];
    const output = sample?.output || '';
    if (inlinePreviewDocuments.get(frame) === output) continue;
    inlinePreviewDocuments.set(frame, output);
    try {
      frame.srcdoc = safePreview(output);
    } catch {
      const source = frame.nextElementSibling;
      if (source?.matches('[data-source-sample]')) source.open = true;
      frame.replaceWith(Object.assign(document.createElement('p'), { className: 'field-note', textContent: '作品预览暂不可用，原文仍可查看和复制。' }));
    }
  }
}

