'use strict';

const stages = ['Ready', 'Plan', 'Execute', 'Review', 'Release'];
const pageSize = 500;
const eventLimit = 300;
let recent = [];
let snapshot;
let changed = new Set();
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

function keyed(node, key) {
  node.dataset.key = key;
  return node;
}

function link(text, href, key, className) {
  const node = keyed(element('a', text, className), key);
  node.setAttribute('href', href);
  return node;
}

function scope() {
  const params = new URLSearchParams(location.search);
  return {workstream: params.get('workstream') || '', label: (params.get('label') || '').trim().replace(/\s+/g, ' ').toLowerCase()};
}

function address(workstream, label) {
  const params = new URLSearchParams();
  if (workstream) params.set('workstream', workstream);
  if (label) params.set('label', label);
  return `?${params}`;
}

function scopeLink(text, workstream, label, key, current = false, className) {
  const node = link(text, address(workstream, label), key, className);
  node.dataset.scope = '';
  if (current) node.setAttribute('aria-current', 'page');
  return node;
}

function details(key, title, children) {
  const node = keyed(element('details'), key);
  node.append(keyed(element('summary', title), `${key}:summary`), ...children);
  return node;
}

function indexByID(values) {
  return new Map(values.map(value => [value.id, value]));
}

function lookupBoard(board) {
  const items = indexByID(board.items);
  const slices = indexByID(board.slices);
  const runs = new Map(board.runs.map(run => [run.id, run.slice_id]));
  const flags = new Map(board.flags.map(flag => [flag.id, flag.subject]));
  for (const event of recent) {
    const data = event.data || {};
    if (event.type === 'slice_claimed') runs.set(data.run_id, data.slice_id);
    if (event.type === 'flag_raised') flags.set(data.flag_id, data.subject);
  }
  return {items, slices, runs, flags};
}

function subjectItem(subject, lookup) {
  if (!subject) return undefined;
  return subject.kind === 'item' ? lookup.items.get(subject.id) : lookup.items.get(lookup.slices.get(subject.id)?.item_id);
}

function scopedBoard(board, workstream, label, lookup) {
  const items = board.items.filter(item => (!workstream || item.workstream === workstream) && (!label || (item.labels || []).includes(label)));
  const itemIDs = new Set(items.map(item => item.id));
  const slices = board.slices.filter(slice => itemIDs.has(slice.item_id));
  const sliceIDs = new Set(slices.map(slice => slice.id));
  return {items, slices, runs: board.runs.filter(run => sliceIDs.has(run.slice_id)),
    flags: board.flags.filter(flag => itemIDs.has(subjectItem(flag.subject, lookup)?.id))};
}

function mark(node, ...keys) {
  if (keys.some(key => changed.has(key))) node.classList.add('changed');
  return node;
}

function switcher(board, selection, lookup) {
  return ['', ...board.workstreams.map(ws => ws.name)].map(name => {
    const view = scopedBoard(board, name, '', lookup);
    return scopeLink(`${name || 'All'} · ${view.runs.length} running · ${view.flags.length} asks`, name, selection.label,
      `ws:${name}`, name === selection.workstream);
  });
}

function labelsView(items, selection) {
  const counts = new Map();
  for (const item of items.filter(item => !['done', 'dropped'].includes(item.status))) {
    for (const label of item.labels || []) counts.set(label, (counts.get(label) || 0) + 1);
  }
  if (!counts.size && !selection.label) return [];
  const labels = [...counts].sort(([left, count], [right, other]) => other - count || left.localeCompare(right));
  if (selection.label && !counts.has(selection.label)) labels.unshift([selection.label, 0]);
  const links = labels.map(([label, count]) => scopeLink(`${label} · ${count}`, selection.workstream, label, `label:${label}`, label === selection.label));
  const result = [scopeLink('Any label', selection.workstream, '', 'label:', !selection.label), ...links.slice(0, 12)];
  if (links.length > 12) {
    const more = details('more-labels', `${links.length - 12} more labels`, links.slice(12));
    if (links.slice(12).some(node => node.hasAttribute('aria-current'))) more.open = true;
    result.push(more);
  }
  return result;
}

function capacity(ws, stage) {
  const key = stage.toLowerCase();
  return `${ws.usage[key]}/${ws.limits[key]}`;
}

function strip(ws) {
  const view = mark(element('div', undefined, 'strip'), `ws:${ws.name}`);
  for (const stage of stages) {
    const key = stage.toLowerCase();
    const used = ws.usage[key], limit = ws.limits[key];
    const tile = element('div', undefined, 'tile');
    tile.setAttribute('aria-label', `${stage}: ${used} of ${limit} used`);
    if (used >= limit) tile.classList.add(used > limit ? 'over' : 'full');
    tile.append(element('strong', stage), element('span', capacity(ws, stage), 'count'));
    if (limit === 0) tile.append(element('p', 'No capacity', 'muted'));
    else if (used > limit) tile.append(element('p', 'Over limit'));
    else if (used === limit) tile.append(element('p', 'Full'));
    if (limit > 0 && limit <= 20) {
      const pips = element('div', undefined, 'pips');
      pips.setAttribute('aria-hidden', 'true');
      for (let slot = 0; slot < Math.min(20, Math.max(limit, used)); slot++) {
        pips.append(element('span', '', `pip${slot >= limit ? ' extra' : slot < used ? ' used' : ''}`));
      }
      tile.append(pips);
    }
    view.append(tile);
  }
  return view;
}

function card(kind, id, title, className = '') {
  const node = mark(element('article', undefined, `card ${className}`.trim()), `${kind}:${id}`);
  node.setAttribute('id', `card-${kind}-${id}`);
  node.append(element('p', id, 'muted'), element('strong', title));
  return node;
}

function itemMeta(item, key) {
  const view = element('div', undefined, 'item-meta');
  const tier = ['ready', 'shelved', 'planning'].includes(item.status) ? item.predicted_tier : item.confirmed_tier;
  view.append(element('p', `${item.ref} · ${item.owner} · tier ${tier}`, 'muted'));
  if (item.labels?.length) {
    const chips = element('div', undefined, 'chips');
    for (const label of item.labels) chips.append(scopeLink(label, item.workstream, label, `${key}:label:${label}`, false, 'chip'));
    view.append(chips);
  }
  return view;
}

function itemCard(item, compact = false) {
  const node = card('item', item.id, item.title, compact ? 'ready-row' : '');
  node.append(itemMeta(item, `item:${item.id}`));
  if (item.status === 'planning') node.append(element('p', item.proposal ? `${item.proposal.length} proposed slices · ${item.approver || 'awaiting approval'}` : 'Planning'));
  if (item.status === 'shelved') node.append(element('p', [item.shelved_reason?.replaceAll('_', ' '), item.claimed_by].filter(Boolean).join(' · '), 'muted'));
  return node;
}

function prView(pr, key) {
  const label = `${pr.repo}#${pr.number}`;
  const match = /^([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)\/([A-Za-z0-9._-]+)$/.exec(pr.repo);
  if (!match || match[0] !== pr.repo || ['.', '..'].includes(match[2]) || !Number.isInteger(pr.number) || pr.number <= 0) return label;
  const node = link(label, `https://github.com/${encodeURIComponent(match[1])}/${encodeURIComponent(match[2])}/pull/${pr.number}`, key);
  node.setAttribute('rel', 'noopener noreferrer');
  return node;
}

function duration(seconds) {
  const whole = Math.max(0, Math.floor(seconds));
  return whole >= 3600 ? `${Math.floor(whole / 3600)}h ${Math.floor(whole % 3600 / 60)}m` : `${Math.floor(whole / 60)}m ${whole % 60}s`;
}

function relative(value) {
  return `${duration((Date.now() + serverOffset - Date.parse(value)) / 1000)} ago`;
}

function leaseState(run) {
  const left = (Date.parse(run.lease_until) - Date.now() - serverOffset) / 1000;
  return {left, state: left <= 0 ? 'lease-expired' : left <= Math.max(60, snapshot.lease_seconds * 0.2) ? 'lease-expiring' : ''};
}

function updateLease({node, parent, run}) {
  const {left, state} = leaseState(run);
  node.textContent = left <= 0 ? `expired ${duration(-left)} ago · stale` : `${duration(Math.ceil(left))} left${state ? ' · expiring' : ''}`;
  parent.classList.toggle('lease-expired', state === 'lease-expired');
  parent.classList.toggle('lease-expiring', state === 'lease-expiring');
}

function sliceCard(slice, lookup, runs) {
  const node = card('slice', slice.id, slice.title);
  const item = lookup.items.get(slice.item_id);
  if (item) node.append(itemMeta(item, `slice:${slice.id}`));
  const slots = [slice.holds_execute && 'Execute', slice.holds_review && 'Review'].filter(Boolean);
  node.append(element('p', `${slice.status.replaceAll('_', ' ')}${slots.length ? ` · holds ${slots.join(' + ')}` : ''}`, 'muted'));
  if (slice.waits_on?.length) node.append(element('p', `waits on ${slice.waits_on.map(id => `${id} (${lookup.slices.get(id)?.status || 'unknown'})`).join(', ')}`, 'warning'));
  if (slice.status === 'stalled') node.append(element('p', `Stalled since ${relative(slice.stalled_at)}`, 'warning'));
  if (slice.rework) node.append(element('p', `Rework · ${(slice.rework_reason || '').replaceAll('_', ' ')}`, 'warning'));
  if (slice.pr) {
    const pr = element('p');
    pr.append(prView(slice.pr, `pr:${slice.id}`));
    node.append(pr, element('p', !slice.pr.checks_recorded ? 'Checks not recorded' : slice.pr.checks_green ? 'Checks passed' : 'Checks failed', slice.pr.checks_green ? 'ok' : 'warning'));
  }
  if (slice.approvers?.length) node.append(element('p', `Approvers: ${slice.approvers.join(', ')}`, 'muted'));
  const run = runs.get(slice.id);
  if (run) {
    mark(node, `run:${run.id}`);
    node.append(element('p', `${run.runner_id} · ${run.id} · started ${relative(run.started_at)}`, 'muted'));
    const lease = element('p', '', 'lease');
    const entry = {node: lease, parent: node, run};
    updateLease(entry);
    countdowns.push(entry);
    node.append(lease);
    if (run.last_note) node.append(element('p', run.last_note));
  }
  return node;
}

function flagCard(flag) {
  const node = card('flag', flag.id, flag.question);
  node.append(element('p', `${flag.owner} · ${flag.ask} · ${flag.subject.id} · waiting ${relative(flag.raised_at)}`, 'muted'));
  return node;
}

function column(ws, stage, cards, waiting = [], waitingTitle = '') {
  const node = element('div', undefined, 'column');
  node.append(element('h4', `${stage} · ${capacity(ws, stage)}`));
  node.append(...(cards.length ? cards : [element('p', `Nothing in ${stage}`, 'muted')]));
  if (waiting.length) node.append(element('p', waitingTitle, 'divider muted'), ...waiting);
  return node;
}

function lane(ws, view, selection, lookup) {
  const node = element('section', undefined, 'lane');
  node.dataset.workstream = ws.name;
  const heading = element('h2');
  heading.append(selection.workstream ? ws.name : scopeLink(ws.name, ws.name, selection.label, `lane:${ws.name}`));
  node.append(heading, strip(ws));
  if (view.flags.length) node.append(element('h3', 'Waiting on people'), ...view.flags.map(flagCard));
  const runs = new Map(view.runs.map(run => [run.slice_id, run]));
  const cards = slices => slices.map(slice => sliceCard(slice, lookup, runs));
  const plan = view.items.filter(item => item.status === 'planning').map(item => itemCard(item));
  const execute = cards(view.slices.filter(slice => ['running', 'flagged', 'stalled'].includes(slice.status) || slice.status === 'queued' && slice.holds_execute));
  const next = cards(view.slices.filter(slice => slice.status === 'queued' && !slice.holds_execute));
  const review = cards(view.slices.filter(slice => ['submitted', 'pr_open', 'in_review', 'approved'].includes(slice.status)));
  const release = view.items.filter(item => item.holds_release).map(item => itemCard(item));
  const waiting = view.items.filter(item => item.status === 'awaiting_release' && !item.holds_release).map(item => itemCard(item));
  if (plan.length || execute.length || next.length || review.length || release.length || waiting.length) {
    const columns = element('div', undefined, 'columns');
    columns.append(column(ws, 'Plan', plan), column(ws, 'Execute', execute, next, 'Up next (not counted)'),
      column(ws, 'Review', review), column(ws, 'Release', release, waiting, 'Awaiting release (not counted)'));
    node.append(element('h3', 'In flight'), columns);
  } else node.append(element('p', 'Nothing in flight', 'muted'));
  if (selection.workstream) node.append(...backlog(ws, view, selection));
  const done = view.items.filter(item => item.status === 'done').length;
  const dropped = view.items.filter(item => item.status === 'dropped').length;
  const merged = view.slices.filter(slice => slice.status === 'merged').length;
  if (done || dropped || merged) node.append(element('p', `${done} done, ${dropped} dropped items and ${merged} merged slices not shown`, 'muted'));
  return node;
}

function backlog(ws, view, selection) {
  const ready = view.items.filter(item => item.status === 'ready').sort((left, right) => Number(right.front) - Number(left.front) || left.rank - right.rank || left.id.localeCompare(right.id, undefined, {numeric: true}));
  const shelved = view.items.filter(item => item.status === 'shelved');
  if (!ready.length && !shelved.length) return [];
  const node = element(selection.workstream ? 'div' : 'section', undefined, 'backlog');
  node.dataset.workstream = ws.name;
  const heading = element(selection.workstream ? 'h3' : 'h2', 'Backlog');
  if (!selection.workstream) heading.append(' · ', scopeLink(ws.name, ws.name, selection.label, `backlog:${ws.name}`));
  node.append(heading, element('p', `${ready.length} Ready`, 'muted'), ...ready.slice(0, 5).map(item => itemCard(item, true)));
  if (ready.length > 5) node.append(details(`ready:${ws.name}`, `${ready.length - 5} more in Ready`, ready.slice(5).map(item => itemCard(item, true))));
  if (shelved.length) node.append(details(`shelved:${ws.name}`, `${shelved.length} shelved`, shelved.map(item => itemCard(item, true))));
  return [node];
}

function attentionView(view, board) {
  const list = element('ul');
  function add(kind, id, text, category = kind) {
    const row = element('li');
    row.append(link(text, `#card-${kind}-${id}`, `attention:${category}:${id}`));
    list.append(row);
  }
  for (const flag of view.flags) add('flag', flag.id, `${flag.owner} · ${flag.id} needs an answer`);
  for (const run of view.runs) {
    const {state} = leaseState(run);
    if (state) add('slice', run.slice_id, `${run.id} · ${state === 'lease-expired' ? 'stale lease' : 'lease expiring soon'}`, 'lease');
  }
  for (const slice of view.slices.filter(slice => slice.status !== 'merged')) {
    if (slice.status === 'stalled' || slice.rework || slice.pr?.checks_recorded && !slice.pr.checks_green) {
      add('slice', slice.id, `${slice.id} · ${slice.status === 'stalled' ? 'stalled' : slice.rework ? 'rework' : 'checks failed'}`);
    }
  }
  for (const [name, used] of Object.entries(board.usage.per_approver)) {
    if (used > board.limits.per_approver) {
      const row = element('li');
      row.append(link(`${name} · over reviewer cap (${used}/${board.limits.per_approver})`, '#reviewers', `attention:reviewer:${name}`));
      list.append(row);
    }
  }
  if (!list.childElementCount) return [];
  const section = element('section');
  section.append(element('h2', `Needs attention · ${list.childElementCount}`), list);
  return [section];
}

function reviewersView(board) {
  const node = element('section');
  const cap = board.limits.per_approver;
  const counts = Object.entries(board.usage.per_approver).sort(([left], [right]) => left.localeCompare(right));
  node.append(element('h2', `Per approver (cap ${cap})`), element('p', counts.length ? counts.map(([name, used]) => `${name} ${used}/${cap}${used > cap ? ' over cap' : ''}`).join(' · ') : 'No PRs in review', 'muted'));
  return node;
}

function eventPlacement(event, lookup) {
  const data = event.data || {};
  const subject = data.subject || lookup.flags.get(data.flag_id);
  const sliceID = data.slice_id || lookup.runs.get(data.run_id) || (subject?.kind === 'slice' ? subject.id : undefined);
  const item = lookup.items.get(data.item_id) || lookup.items.get(lookup.slices.get(sliceID)?.item_id) || subjectItem(subject, lookup);
  return {workstream: data.workstream || item?.workstream || lookup.slices.get(sliceID)?.workstream, item};
}

function eventSentence(event, lookup) {
  const data = event.data || {};
  const actor = event.actor || 'Metronome';
  const item = lookup.items.get(data.item_id);
  const name = item ? `${item.ref} “${item.title}”` : data.item_id;
  const slice = data.slice_id;
  const run = data.run_id;
  const ws = data.workstream || item?.workstream;
  const flag = data.flag_id;
  let text;
  if (name) {
    switch (event.type) {
      case 'item_added': text = `${actor} added ${name}${ws ? ` to ${ws}` : ''}`; break;
      case 'item_updated': text = `${name} was updated from the source`; break;
      case 'item_shelved': text = `${name} was shelved${data.reason ? `: ${data.claimed_by ? `claimed by ${data.claimed_by} in the source` : data.reason === 'missing' ? 'no longer in the source' : data.reason.replaceAll('_', ' ')}` : ''}`; break;
      case 'item_restored': text = `${name} returned to Ready`; break;
      case 'plan_started': text = `${actor} started planning ${name}`; break;
      case 'plan_proposed': if (Array.isArray(data.slices) && data.approver) text = `${actor} proposed ${data.slices.length} slices for ${name} for ${data.approver} to approve`; break;
      case 'plan_approved': if (Array.isArray(data.slice_ids) && Number.isInteger(data.confirmed_tier)) text = `${actor} approved the plan for ${name} at tier ${data.confirmed_tier}: ${data.slice_ids.join(', ')}`; break;
      case 'plan_dropped': text = `${actor} dropped the plan for ${name}`; break;
      case 'item_parked': text = `${name} went back to the front of Ready`; break;
      case 'item_awaits_release': text = `${name} is awaiting release`; break;
      case 'release_slot_taken': text = `${name} took a Release slot`; break;
      case 'item_done': text = `${name} is done`; break;
      case 'release_done': text = `${actor} completed release for ${name}`; break;
    }
  }
  if (slice) {
    switch (event.type) {
      case 'slice_claimed': if (run && data.runner_id) text = `${data.runner_id} claimed ${slice} as run ${run}`; break;
      case 'slice_parked': text = `${slice} was parked`; break;
      case 'pr_opened': if (data.repo && data.number) text = `${actor} recorded PR ${data.repo}#${data.number} for ${slice}`; break;
      case 'checks_completed': if (typeof data.success === 'boolean' && data.head_sha) text = `Checks ${data.success ? 'passed' : 'failed'} for ${slice} at ${data.head_sha.slice(0, 7)}`; break;
      case 'pr_ready_for_review': text = `${slice} is ready for review`; break;
      case 'rework_fixed': if (data.head_sha) text = `${slice} is back in review at ${data.head_sha.slice(0, 7)}${data.by ? `, pushed by ${data.by}` : ''}`; break;
      case 'pr_approved': if (data.by && data.head_sha) text = `${data.by} approved ${slice} at ${data.head_sha.slice(0, 7)}`; break;
      case 'approval_withdrawn': text = `Approval was withdrawn for ${slice}`; break;
      case 'pr_changes_requested': if (data.by) text = `${data.by} requested changes on ${slice}`; break;
      case 'pr_merged': text = `${slice} was merged`; break;
    }
  }
  switch (event.type) {
    case 'workstream_created': if (ws) text = `${actor} created workstream ${ws}`; break;
    case 'workstream_limits_set': if (ws && data.limits) text = `${actor} set ${ws} limits: ${stages.map(stage => `${stage.toLowerCase()} ${data.limits[stage.toLowerCase()]}`).join(', ')}`; break;
    case 'flag_raised': if (data.subject?.id && data.owner && data.question) text = `${actor} asked ${data.owner} to ${data.ask || 'answer'} on ${data.subject.id}: “${data.question}”`; break;
    case 'flag_resolved': if (flag && data.answer !== undefined) text = `${actor} answered ${flag}: “${data.answer}”`; break;
    case 'flag_timed_out': if (flag) text = `${flag} timed out`; break;
    case 'heartbeat': if (run) text = data.note ? `${run} checked in: ${data.note}` : `${run} renewed its lease`; break;
    case 'run_closed': if (run) {
      const reason = {flagged: 'paused for an ask', submitted: `was submitted${data.branch ? ` on branch ${data.branch}` : ''}`, released: 'was dropped', lease_expired: 'lease expired'}[data.reason];
      if (reason) text = `${run} ${reason}`;
    } break;
    case 'publisher_advanced': if (ws && data.publisher) text = `${actor} advanced publisher ${data.publisher} in ${ws}`; break;
  }
  text = text || `${actor} recorded ${event.type.replaceAll('_', ' ')}`;
  return text.length > 140 ? `${text.slice(0, 139)}…` : text;
}

function eventsView(selection, lookup) {
  const list = element('ol', undefined, 'activity');
  let unplaced = 0;
  for (const event of [...recent].reverse()) {
    const {workstream, item} = eventPlacement(event, lookup);
    if (selection.workstream && workstream && workstream !== selection.workstream) continue;
    if ((selection.workstream && !workstream) || (selection.label && !item)) {
      unplaced++;
      continue;
    }
    if (selection.label && !(item.labels || []).includes(selection.label)) continue;
    if (list.childElementCount >= 30) continue;
    const row = mark(element('li'), `event:${event.id}`);
    const time = element('time', relative(event.at), 'muted');
    time.setAttribute('datetime', event.at);
    time.setAttribute('title', new Date(event.at).toLocaleString());
    row.append(time, element('span', ` · ${!selection.workstream && workstream ? `${workstream} · ` : ''}${eventSentence(event, lookup)}`));
    list.append(row);
  }
  const nodes = [list.childElementCount ? list : element('p', 'No matching events', 'muted')];
  if (unplaced) nodes.push(element('p', `${unplaced} recent events could not be placed in this view`, 'muted'));
  return nodes;
}

function signatures(board) {
  const result = new Map();
  for (const [kind, values] of [['item', board.items], ['slice', board.slices], ['run', board.runs], ['flag', board.flags], ['ws', board.workstreams]]) {
    for (const value of values) result.set(`${kind}:${value.id || value.name}`, JSON.stringify(value));
  }
  return result;
}

function changes(board, events) {
  const result = new Set();
  if (!snapshot) return result;
  const before = signatures(snapshot), after = signatures(board);
  for (const [key, value] of after) if (before.get(key) !== value) result.add(key);
  for (const key of before.keys()) if (!after.has(key)) result.add(key);
  const previousEvents = new Set(recent.map(event => event.id));
  for (const event of events) if (!previousEvents.has(event.id)) result.add(`event:${event.id}`);
  return result;
}

function render() {
  if (!snapshot) return;
  const focusKey = document.activeElement?.dataset.key;
  const expanded = new Map([...document.querySelectorAll('details')].map(node => [node.dataset.key, node.open]));
  const selection = scope();
  const lookup = lookupBoard(snapshot);
  const scoped = scopedBoard(snapshot, selection.workstream, selection.label, lookup);
  const unlabelled = scopedBoard(snapshot, selection.workstream, '', lookup);
  const streams = snapshot.workstreams.filter(ws => !selection.workstream || ws.name === selection.workstream);
  countdowns = [];
  const groups = streams.map(ws => ({ws, view: scopedBoard(snapshot, ws.name, selection.label, lookup)}));
  const lanes = groups.map(({ws, view}) => lane(ws, view, selection, lookup));
  if (!selection.workstream) lanes.push(...groups.flatMap(({ws, view}) => backlog(ws, view, selection)));
  if (!streams.length) {
    const notice = element('p', `No workstream named ${selection.workstream}. `, 'warning');
    notice.append(scopeLink('Show All', '', selection.label, 'unknown-workstream'));
    lanes.push(notice);
  } else if (selection.label && !scoped.items.length) {
    const notice = element('p', `No items labelled ${selection.label}. `, 'warning');
    notice.append(scopeLink('Clear label', selection.workstream, '', 'unknown-label'));
    lanes.unshift(notice);
  }
  const views = {workstreams: switcher(snapshot, selection, lookup), labels: labelsView(unlabelled.items, selection),
    attention: attentionView(scoped, snapshot), lanes, reviewers: [reviewersView(snapshot)], events: eventsView(selection, lookup)};
  for (const [id, nodes] of Object.entries(views)) document.getElementById(id).replaceChildren(...nodes);
  for (const node of document.querySelectorAll('details')) if (expanded.has(node.dataset.key)) node.open = expanded.get(node.dataset.key);
  if (focusKey) {
    const focused = [...document.querySelectorAll('[data-key]')].find(node => node.dataset.key === focusKey);
    if (focused) {
      const container = focused.closest('details');
      if (container && focused.tagName !== 'SUMMARY') container.open = true;
      focused.focus({preventScroll: true});
    }
  }
}

function setStatus(text, warning = false) {
  const status = document.getElementById('status');
  if (status.textContent !== text) status.textContent = text;
  status.classList.toggle('warning', warning);
}

function changeScope() {
  changed = new Set();
  if (!document.getElementById('status').classList.contains('warning')) setStatus('');
  render();
}

document.addEventListener('click', event => {
  const anchor = event.target.closest('a[data-scope]');
  if (!anchor || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  event.preventDefault();
  history.pushState(null, '', anchor.getAttribute('href'));
  changeScope();
});
window.addEventListener('popstate', changeScope);

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
    if (page.some((event, index) => event.seq <= (index ? page[index - 1].seq : after))) throw new Error('Event sequence changed; retrying');
    events = events.concat(page).slice(-eventLimit);
    if (page.length < pageSize) return events;
    after = page[page.length - 1].seq;
  }
}

async function refresh() {
  if (refreshing || document.hidden) return;
  clearTimeout(refreshTimer);
  refreshing = true;
  try {
    const {data: board, date} = await getJSON('/v1/board');
    const serverTime = date ? Date.parse(date) : NaN;
    const offset = Number.isFinite(serverTime) ? serverTime - Date.now() : 0;
    const events = await readEvents();
    changed = changes(board, events);
    snapshot = board;
    recent = events;
    serverOffset = offset;
    render();
    setStatus(changed.size && lastUpdated ? `${changed.size} changes since ${lastUpdated.toLocaleTimeString()}` : '');
    lastUpdated = new Date();
    document.getElementById('updated').textContent = `Last updated ${lastUpdated.toLocaleTimeString()}`;
  } catch (error) {
    setStatus(lastUpdated ? `Not updated since ${lastUpdated.toLocaleTimeString()}: ${error.message}` : `Not yet updated: ${error.message}`, true);
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
  if (!document.hidden) for (const entry of countdowns) updateLease(entry);
}, 1000);
refresh();
