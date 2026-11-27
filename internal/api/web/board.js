'use strict';

const stages = ['Ready', 'Plan', 'Execute', 'Review', 'Release'];
const pageSize = 500;
const eventLimit = 30;
let recent = [];
let lastUpdated = null;
let serverOffset = 0;
let countdowns = [];
let refreshTimer;
let refreshing = false;

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}

function table(columns, rows, empty) {
  if (!rows.length) return element('p', empty, 'muted');
  const wrapper = element('div', undefined, 'scroll');
  const grid = element('table');
  const head = grid.createTHead().insertRow();
  for (const [label, className] of columns) {
    const cell = element('th', label, className);
    cell.setAttribute('scope', 'col');
    head.append(cell);
  }
  const body = grid.createTBody();
  for (const row of rows) {
    const tr = body.insertRow();
    row.forEach((value, index) => {
      const cell = tr.insertCell();
      cell.className = columns[index][1] || '';
      if (value instanceof Node) cell.append(value);
      else cell.textContent = value;
    });
  }
  wrapper.append(grid);
  return wrapper;
}

function limitsView(board) {
  const view = element('div');
  const tiles = element('div', undefined, 'tiles');
  for (const stage of stages) {
    const key = stage.toLowerCase();
    const used = board.usage[key];
    const cap = board.limits[key];
    const tile = element('div', undefined, 'tile');
    tile.append(element('strong', stage), element('span', `${used}/${cap}`));
    if (used >= cap) tile.append(element('span', ' · full', 'warning'));
    tiles.append(tile);
  }
  const cap = board.limits.per_approver;
  view.append(tiles, element('p', `Per approver (cap ${cap})`));
  const approvers = Object.entries(board.usage.per_approver).sort(([left], [right]) => left.localeCompare(right));
  view.append(table([['Approver'], ['Usage']], approvers.map(([name, used]) => [name,
    element('span', `${used}/${cap}${used > cap ? ' · over cap' : ''}`, used > cap ? 'warning' : '')
  ]), 'No PRs in review'));
  return view;
}

function activeTable(all, active, columns, rows, empty, hiddenLabel) {
  const view = element('div');
  view.append(table(columns, active.map(rows), empty));
  if (all.length > active.length) {
    view.append(element('p', `${all.length - active.length} ${hiddenLabel} not shown`, 'muted'));
  }
  return view;
}

function itemsView(items) {
  return activeTable(items, items.filter(item => !['done', 'dropped'].includes(item.status)),
    [['ID / Ref'], ['Status'], ['Tier'], ['Owner'], ['Labels', 'wrap'], ['Title', 'wrap']],
    item => [[item.id, item.ref].filter(Boolean).join(' '), item.status, item.predicted_tier, item.owner, (item.labels || []).join(', ') || '-', item.title],
    'No items', 'done or dropped');
}

function prView(pr) {
  if (!pr) return '-';
  const label = `${pr.repo}#${pr.number}`;
  const match = /^([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)\/([A-Za-z0-9._-]+)$/.exec(pr.repo);
  if (!match || match[0] !== pr.repo || ['.', '..'].includes(match[2]) || !Number.isInteger(pr.number) || pr.number <= 0) return label;
  const link = element('a', label);
  link.setAttribute('href', `https://github.com/${encodeURIComponent(match[1])}/${encodeURIComponent(match[2])}/pull/${pr.number}`);
  link.setAttribute('rel', 'noopener noreferrer');
  return link;
}

function slicesView(slices) {
  return activeTable(slices, slices.filter(slice => slice.status !== 'merged'),
    [['ID'], ['Status'], ['Slots'], ['Run'], ['PR'], ['Title', 'wrap']], slice => {
      const slots = [slice.holds_execute && 'execute', slice.holds_review && 'review'].filter(Boolean).join('+');
      return [slice.id, slice.status + (slice.rework ? ' (rework)' : ''), slots || '-', slice.run_id || '-', prView(slice.pr), slice.title];
    }, 'No slices', 'merged');
}

function duration(seconds) {
  const whole = Math.max(0, Math.floor(seconds));
  return whole >= 3600 ? `${Math.floor(whole / 3600)}h ${Math.floor(whole % 3600 / 60)}m ${whole % 60}s` :
    `${Math.floor(whole / 60)}m ${whole % 60}s`;
}

function relative(value, offset) {
  return `${duration((Date.now() + offset - Date.parse(value)) / 1000)} ago`;
}

function updateLease(node, deadline, offset) {
  const seconds = (deadline - Date.now() - offset) / 1000;
  node.textContent = seconds > 0 ? duration(Math.ceil(seconds)) : `expired ${duration(-seconds)} ago`;
  node.className = seconds <= 0 ? 'warning' : '';
}

function runsView(runs, offset, leases) {
  return table([['ID'], ['Slice'], ['Runner'], ['Started', 'secondary'], ['Lease left'], ['Last note', 'wrap secondary']],
    runs.map(run => {
      const lease = element('span');
      const deadline = Date.parse(run.lease_until);
      updateLease(lease, deadline, offset);
      leases.push({node: lease, deadline});
      return [run.id, run.slice_id, run.runner_id, relative(run.started_at, offset), lease, run.last_note || '-'];
    }), 'No open runs');
}

function asksView(asks, offset) {
  return table([['ID'], ['Owner'], ['Type'], ['On'], ['Waiting'], ['Question', 'wrap']],
    asks.map(ask => [ask.id, ask.owner, ask.ask, `${ask.subject.kind} ${ask.subject.id}`, relative(ask.raised_at, offset), ask.question]),
    'No open asks');
}

function eventSummary(event) {
  const data = event.data || {};
  const format = (fields, build) => fields.every(key => data[key] !== undefined && data[key] !== null) ? build() : JSON.stringify(data);
  let summary;
  switch (event.type) {
    case 'item_added': summary = format(['item_id', 'ref', 'title'], () => `${data.item_id} ${data.ref}: ${data.title}`); break;
    case 'plan_started': case 'plan_dropped': case 'item_parked': case 'item_awaits_release': case 'release_slot_taken': case 'release_done':
      summary = format(['item_id'], () => data.item_id); break;
    case 'plan_proposed': summary = format(['item_id', 'slices'], () => `${data.item_id}: ${data.slices.length} slices`); break;
    case 'plan_approved': summary = format(['item_id', 'confirmed_tier', 'slice_ids'], () => `${data.item_id} tier ${data.confirmed_tier}: ${data.slice_ids.join(', ')}`); break;
    case 'flag_raised': summary = format(['flag_id', 'ask', 'owner', 'question'], () => `${data.flag_id} ${data.ask} for ${data.owner}: ${data.question}`); break;
    case 'flag_resolved': summary = format(['flag_id', 'answer'], () => `${data.flag_id}: ${data.answer}`); break;
    case 'flag_timed_out': summary = format(['flag_id'], () => data.flag_id); break;
    case 'slice_claimed': summary = format(['run_id', 'slice_id', 'runner_id'], () => `${data.run_id} took ${data.slice_id} (${data.runner_id})`); break;
    case 'heartbeat': summary = format(['run_id'], () => data.run_id + (data.note ? `: ${data.note}` : '')); break;
    case 'run_closed': summary = format(['run_id', 'reason'], () => `${data.run_id} ${data.reason}${data.branch ? ` on ${data.branch}` : ''}`); break;
    case 'slice_parked': case 'pr_ready_for_review': case 'pr_merged': summary = format(['slice_id'], () => data.slice_id); break;
    case 'pr_opened': summary = format(['slice_id', 'repo', 'number'], () => `${data.slice_id} ${data.repo}#${data.number}`); break;
    case 'checks_completed': summary = format(['slice_id', 'success', 'head_sha'], () => `${data.slice_id} ${data.success ? 'green' : 'red'} at ${data.head_sha.slice(0, 7)}`); break;
    case 'pr_approved': summary = format(['slice_id', 'by', 'head_sha'], () => `${data.slice_id} by ${data.by} at ${data.head_sha.slice(0, 7)}`); break;
    case 'pr_changes_requested': summary = format(['slice_id', 'by'], () => `${data.slice_id} by ${data.by}`); break;
    default: summary = JSON.stringify(data);
  }
  return summary.length > 140 ? summary.slice(0, 139) + '…' : summary;
}

function eventsView(events) {
  return table([['Time'], ['Actor', 'secondary'], ['Type'], ['Summary', 'wrap']], [...events].reverse().map(event => {
    const date = new Date(event.at);
    const label = (date.toDateString() === new Date().toDateString() ? '' : date.toLocaleDateString() + ' ') + date.toLocaleTimeString();
    const time = element('time', label);
    time.setAttribute('title', event.at);
    time.setAttribute('datetime', event.at);
    return [time, event.actor, event.type, eventSummary(event)];
  }), 'No events');
}

async function getJSON(path) {
  const response = await fetch(path, {cache: 'no-store'});
  if (!response.ok) throw new Error(`${path}: HTTP ${response.status}`);
  return {data: await response.json(), date: response.headers.get('Date')};
}

async function eventPage(after, limit) {
  return (await getJSON(`/v1/events?after=${after}&limit=${limit}`)).data;
}

async function readEvents() {
  let events = recent.slice();
  let last = events[events.length - 1];
  if (last) {
    const anchor = (await eventPage(last.seq - 1, 1))[0];
    if (!anchor || anchor.seq !== last.seq || anchor.id !== last.id) {
      events = [];
      last = undefined;
    }
  }
  let after = last ? last.seq : 0;
  while (true) {
    const page = await eventPage(after, pageSize);
    if (page.some((event, index) => event.seq <= (index ? page[index - 1].seq : after))) {
      throw new Error('Event sequence changed; retrying');
    }
    events = events.concat(page).slice(-eventLimit);
    if (page.length < pageSize) return events;
    after = page[page.length - 1].seq;
  }
}

async function refresh() {
  if (refreshing || document.hidden) return;
  clearTimeout(refreshTimer);
  refreshing = true;
  const status = document.getElementById('status');
  try {
    const {data: board, date} = await getJSON('/v1/board');
    const serverTime = date ? Date.parse(date) : NaN;
    const offset = Number.isFinite(serverTime) ? serverTime - Date.now() : 0;
    const events = await readEvents();
    const leases = [];
    const views = {
      limits: limitsView(board), items: itemsView(board.items), slices: slicesView(board.slices),
      runs: runsView(board.runs, offset, leases), asks: asksView(board.flags, offset), events: eventsView(events)
    };
    for (const [id, view] of Object.entries(views)) document.getElementById(id).replaceChildren(view);
    recent = events;
    serverOffset = offset;
    countdowns = leases;
    lastUpdated = new Date();
    status.textContent = `Last updated ${lastUpdated.toLocaleTimeString()}`;
    status.className = '';
  } catch (error) {
    status.textContent = lastUpdated ? `Not updated since ${lastUpdated.toLocaleTimeString()}: ${error.message}` : `Not yet updated: ${error.message}`;
    status.className = 'warning';
  } finally {
    refreshing = false;
    if (!document.hidden) refreshTimer = setTimeout(refresh, 5000);
  }
}

document.addEventListener('visibilitychange', () => {
  clearTimeout(refreshTimer);
  if (!document.hidden) refresh();
});
setInterval(() => {
  if (!document.hidden) for (const {node, deadline} of countdowns) updateLease(node, deadline, serverOffset);
}, 1000);
refresh();
