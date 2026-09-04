// Load test for POST /uploads, at four levels of concurrency in sequence.
//
// Each level is its own scenario tagged with the number of virtual users, so
// the summary reports latency and throughput per level rather than averaging a
// ramp into one meaningless figure. The levels run one after another, with a
// gap between them so the queue drains and each starts from a quiet system.
//
// Run it against a stack that is already up:
//
//	k6 run test/load/uploads.js                    # against localhost:8080
//	k6 run -e BASE_URL=http://api:8080 uploads.js  # from inside the compose network
//
// The API's rate limiter allows 5 requests a second per client, which is far
// below what this generates: raise IMAGEFORGE_RATE_LIMIT on the API before a
// run, or the number being measured is the limiter rather than the pipeline.
// Every virtual user authenticates as its own client, so the limit applies per
// user rather than to the run as a whole.
import http from 'k6/http';
import { check, sleep } from 'k6';
import exec from 'k6/execution';
import { Trend, Counter } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

// The concurrency levels to walk through, and how long to hold each one.
const LEVELS = (__ENV.LEVELS || '5,10,20,40').split(',').map(Number);
const HOLD_SECONDS = Number(__ENV.HOLD_SECONDS || 30);
// Long enough for the worker to finish what the previous level queued, so a
// level is not measured against a backlog the one before it left behind.
const GAP_SECONDS = Number(__ENV.GAP_SECONDS || 15);

// The upload payload: a 1600x1200 JPEG of about 480KB, which is a plausible
// photo rather than a token image that would measure request overhead only.
const photo = open('./fixtures/photo.jpg', 'b');

// The transformation asked for. jpeg is the one format both image backends
// support, so a run means the same thing whichever the worker was built with.
const SPEC = JSON.stringify({
  width: Number(__ENV.WIDTH || 800),
  format: __ENV.FORMAT || 'jpeg',
  quality: Number(__ENV.QUALITY || 82),
  strip_metadata: true,
});

// One in POLL_EVERY uploads is followed to completion, to measure the pipeline
// rather than the API. Polling costs requests of its own, so it is sampled
// rather than done for every job.
const POLL_EVERY = Number(__ENV.POLL_EVERY || 20);
const POLL_TIMEOUT_SECONDS = Number(__ENV.POLL_TIMEOUT_SECONDS || 60);

// End-to-end time from accepted upload to a job in a terminal state. This is
// the number a user would feel; http_req_duration is only the accepting half.
const jobLatency = new Trend('job_completion_seconds');
const jobsCompleted = new Counter('jobs_completed');
const jobsFailed = new Counter('jobs_failed');
const jobsStillQueued = new Counter('jobs_still_queued');
const rateLimited = new Counter('uploads_rate_limited');

// Build one scenario per level, each starting after the previous has finished.
const scenarios = {};
LEVELS.forEach((vus, index) => {
  scenarios[`c${vus}`] = {
    executor: 'constant-vus',
    vus,
    duration: `${HOLD_SECONDS}s`,
    startTime: `${index * (HOLD_SECONDS + GAP_SECONDS)}s`,
    gracefulStop: `${GAP_SECONDS}s`,
    tags: { concurrency: String(vus) },
    exec: 'upload',
  };
});

// A threshold per level, because a threshold is what makes k6 print the
// observed value for a tagged subset in the end-of-test summary. The bounds
// are deliberately loose: they are there to fail a run that has fallen over,
// not to assert a particular machine's performance.
const thresholds = {
  'http_req_failed{endpoint:upload}': ['rate<0.01'],
  checks: ['rate>0.99'],
};
LEVELS.forEach((vus) => {
  thresholds[`http_req_duration{endpoint:upload,concurrency:${vus}}`] = ['p(95)<5000'];
  thresholds[`http_reqs{endpoint:upload,concurrency:${vus}}`] = ['count>0'];
  // Not a bound anyone could fail: a threshold is simply the only way to make
  // k6 print a tagged subset of a metric in the summary, and the per-level
  // completion time is the most interesting number this test produces. It is
  // deliberately not asserted, because once uploads outrun the workers the
  // queue absorbs the difference and this figure grows with the backlog —
  // which is the queue working, not the system failing.
  thresholds[`job_completion_seconds{concurrency:${vus}}`] = ['p(95)>=0'];
});

export const options = { scenarios, thresholds, discardResponseBodies: false };

// Each virtual user authenticates once and keeps its token. The client id
// includes the user number so the per-client rate limiter treats them
// separately, as it would separate callers in production.
let token = null;

function authenticate() {
  if (token !== null) {
    return token;
  }

  const res = http.post(
    `${BASE_URL}/auth/token`,
    JSON.stringify({ client_id: `load-${exec.vu.idInTest}` }),
    { headers: { 'Content-Type': 'application/json' }, tags: { endpoint: 'auth' } },
  );

  check(res, { 'token issued': (r) => r.status === 200 });
  token = res.json('access_token');
  return token;
}

export function upload() {
  const bearer = { Authorization: `Bearer ${authenticate()}` };

  const res = http.post(
    `${BASE_URL}/uploads`,
    {
      file: http.file(photo, 'photo.jpg', 'image/jpeg'),
      spec: SPEC,
    },
    { headers: bearer, tags: { endpoint: 'upload' } },
  );

  if (res.status === 429) {
    // Not a server failure: the limiter did exactly what it was configured to
    // do. It is counted separately so a run that measured the limiter instead
    // of the pipeline is obvious in the summary.
    rateLimited.add(1);
    sleep(1);
    return;
  }

  const accepted = check(res, {
    'upload accepted': (r) => r.status === 202,
    'job id returned': (r) => r.status === 202 && typeof r.json('id') === 'string',
  });
  if (!accepted) {
    return;
  }

  if (exec.scenario.iterationInTest % POLL_EVERY === 0) {
    followToCompletion(res.json('id'), bearer);
  }
}

// followToCompletion polls one job until it reaches a terminal state, and
// records how long that took.
function followToCompletion(jobID, bearer) {
  const startedAt = Date.now();

  for (;;) {
    const elapsed = (Date.now() - startedAt) / 1000;
    if (elapsed > POLL_TIMEOUT_SECONDS) {
      // Still queued rather than broken. At concurrency levels above what the
      // worker pool can drain this is the expected outcome, so it is counted
      // and not treated as a failure.
      jobsStillQueued.add(1);
      return;
    }

    const res = http.get(`${BASE_URL}/jobs/${jobID}`, {
      headers: bearer,
      tags: { endpoint: 'poll' },
    });
    const status = res.status === 200 ? res.json('status') : null;

    if (status === 'done') {
      jobLatency.add((Date.now() - startedAt) / 1000);
      jobsCompleted.add(1);
      return;
    }
    if (status === 'failed') {
      jobsFailed.add(1);
      check(res, { 'job did not fail': () => false });
      return;
    }

    // The front-end polls at this interval too, so the load this adds looks
    // like a real client watching its job.
    sleep(1.5);
  }
}

// setup runs once, before any load: it fails the run early and clearly if the
// stack is not actually up, rather than reporting a hundred connection errors.
export function setup() {
  const res = http.get(`${BASE_URL}/readyz`);
  if (res.status !== 200) {
    exec.test.abort(`the API at ${BASE_URL} is not ready (readyz returned ${res.status})`);
  }
}
