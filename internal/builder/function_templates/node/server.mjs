// space-elevator function adapter (Node.js, stdlib only).
//
// Serves an HTTP endpoint that maps each request to an AWS API Gateway
// (HTTP API, payload 2.0) proxy event and calls the user's handler:
//
//   export const handler = async (event, context) => { ... }
//
// The handler's return value becomes the HTTP response. Return an object
// with a "statusCode" key to control status/headers/body, or any other
// value to be serialized as a 200 JSON response.
//
// Environment:
//   SE_FUNCTION_ENTRYPOINT  file:handler  (default "index.js:handler")
//   SE_FUNCTION_APPDIR      directory holding the user code (default /app)
//   PORT                    listen port (default 8080)

import http from 'node:http';
import path from 'node:path';
import { randomUUID } from 'node:crypto';
import { pathToFileURL } from 'node:url';

const ENTRY = process.env.SE_FUNCTION_ENTRYPOINT || 'index.js:handler';
const APP_DIR = process.env.SE_FUNCTION_APPDIR || '/app';
const PORT = parseInt(process.env.PORT || '8080', 10);

function parseEntry(entry) {
  const i = entry.lastIndexOf(':');
  if (i < 0) return { file: entry, symbol: 'handler' };
  return { file: entry.slice(0, i) || entry, symbol: entry.slice(i + 1) || 'handler' };
}

const { file, symbol } = parseEntry(ENTRY);
let handler = null;
let handlerErr = null;

async function loadHandler() {
  try {
    const mod = await import(pathToFileURL(path.join(APP_DIR, file)).href);
    if (typeof mod[symbol] === 'function') return mod[symbol];
    if (typeof mod.default === 'function' && symbol === 'handler') return mod.default;
    if (mod.default && typeof mod.default[symbol] === 'function') return mod.default[symbol];
    throw new Error(`handler '${symbol}' not found in ${file}`);
  } catch (err) {
    handlerErr = err;
    console.error('failed to load handler %s: %s', ENTRY, err && err.message);
    return null;
  }
}

function makeContext() {
  const deadline = Date.now() + parseInt(process.env.SE_FUNCTION_TIMEOUT || '900', 10) * 1000;
  const name = process.env.SE_FUNCTION_NAME || 'function';
  return {
    functionName: name,
    functionVersion: '$LATEST',
    memoryLimitInMB: parseInt(process.env.SE_FUNCTION_MEMORY_MB || '512', 10),
    awsRequestId: randomUUID(),
    logGroupName: '/space-elevator/' + name,
    logStreamName: 'local',
    invokedFunctionArn: 'arn:aws:lambda:local:0:function:' + name,
    getRemainingTimeInMillis: () => Math.max(0, deadline - Date.now()),
  };
}

function readBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on('data', c => {
      size += c.length;
      if (size > 32 * 1024 * 1024) {
        reject(new Error('request body too large'));
        req.destroy();
        return;
      }
      chunks.push(c);
    });
    req.on('end', () => resolve(Buffer.concat(chunks)));
    req.on('error', reject);
  });
}

function buildEvent(req, rawBody) {
  const url = new URL(req.url, 'http://localhost');
  const params = {};
  for (const [k, v] of url.searchParams) {
    if (k in params) {
      params[k] = Array.isArray(params[k]) ? [...params[k], v] : [params[k], v];
    } else {
      params[k] = v;
    }
  }
  let body = null;
  let isBase64Encoded = false;
  if (rawBody.length > 0) {
    body = rawBody.toString('utf8');
    // Round-trip check: invalid UTF-8 means binary; base64 it.
    if (Buffer.from(body, 'utf8').length !== rawBody.length) {
      body = rawBody.toString('base64');
      isBase64Encoded = true;
    }
  }
  const headers = {};
  for (const [k, v] of Object.entries(req.headers)) {
    headers[k] = Array.isArray(v) ? v.join(', ') : v;
  }
  return {
    version: '2.0',
    routeKey: '$default',
    // payload 2.0 fields plus the payload 1.0 style aliases
    // (httpMethod/path) so either handler convention works.
    rawPath: url.pathname,
    rawQueryString: url.search.slice(1),
    httpMethod: req.method,
    path: url.pathname,
    headers,
    queryStringParameters: Object.keys(params).length ? params : null,
    body,
    isBase64Encoded,
    requestContext: {
      accountId: 'local',
      stage: '$default',
      requestId: randomUUID(),
      http: {
        method: req.method,
        path: url.pathname,
        protocol: 'HTTP/' + req.httpVersion,
        sourceIp: req.socket.remoteAddress || '',
        userAgent: headers['user-agent'] || '',
      },
    },
  };
}

function send(res, status, headers, bodyBytes) {
  const out = Buffer.isBuffer(bodyBytes) ? bodyBytes : Buffer.from(String(bodyBytes));
  for (const [k, v] of Object.entries(headers || {})) {
    const lk = k.toLowerCase();
    if (lk === 'content-length' || lk === 'transfer-encoding' || lk === 'connection') continue;
    if (Array.isArray(v)) { for (const item of v) res.setHeader(k, item); }
    else { res.setHeader(k, v); }
  }
  if (!res.hasHeader('Content-Type')) res.setHeader('Content-Type', 'application/json');
  res.setHeader('Content-Length', out.length);
  res.writeHead(status);
  res.end(out);
}

async function respond(res, result) {
  if (result && typeof result === 'object' && 'statusCode' in result) {
    const status = parseInt(result.statusCode, 10) || 200;
    const headers = { ...(result.headers || {}) };
    if (result.cookies) headers['Set-Cookie'] = result.cookies;
    let body = result.body;
    let bytes;
    if (result.isBase64Encoded) {
      bytes = Buffer.from(body == null ? '' : String(body), 'base64');
    } else if (body && typeof body === 'object') {
      bytes = Buffer.from(JSON.stringify(body));
      if (!headers['Content-Type']) headers['Content-Type'] = 'application/json';
    } else {
      bytes = Buffer.from(body == null ? '' : String(body));
    }
    send(res, status, headers, bytes);
    return;
  }
  if (result === undefined) {
    send(res, 200, { 'Content-Type': 'application/json' }, 'null');
    return;
  }
  send(res, 200, { 'Content-Type': 'application/json' }, JSON.stringify(result));
}

const server = http.createServer(async (req, res) => {
  if (req.url === '/__se/health') {
    if (handler) {
      send(res, 200, { 'Content-Type': 'application/json' }, '{"status":"ok"}');
    } else {
      const payload = JSON.stringify({ status: 'error', error: handlerErr ? handlerErr.message : 'loading' });
      send(res, 503, { 'Content-Type': 'application/json' }, payload);
    }
    return;
  }
  if (!handler) {
    const msg = handlerErr ? handlerErr.message : 'not loaded';
    send(res, 500, { 'Content-Type': 'application/json' },
      JSON.stringify({ errorMessage: 'handler unavailable: ' + msg, errorType: 'RuntimeError' }));
    return;
  }
  try {
    const raw = await readBody(req);
    const event = buildEvent(req, raw);
    const result = await handler(event, makeContext());
    await respond(res, result);
  } catch (err) {
    console.error(err);
    send(res, 500, { 'Content-Type': 'application/json' },
      JSON.stringify({ errorMessage: String(err && err.message || err), errorType: (err && err.name) || 'Error' }));
  }
});

handler = await loadHandler();
if (handler) {
  console.error('loaded handler %s', ENTRY);
} else {
  console.error('handler %s unavailable', ENTRY);
}
server.listen(PORT, '0.0.0.0', () => {
  console.error('function adapter listening on :%d', PORT);
});
