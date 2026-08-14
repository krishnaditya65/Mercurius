import json
import threading
import urllib.error
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest

from quantengine.latencyBenchmarkDashboard import (
    LatencyBenchmarkResult,
    buildLatencyHistogramAndPercentiles,
    checkHttpEndpointIsReachable,
    compareLatencyAcrossVenues,
    measureRoundTripTimeSamplesOverHttp,
)


class _EchoHandler(BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        pass

    def do_GET(self):
        body = b'{"status":"ok"}'
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        contentLength = int(self.headers.get("Content-Length", 0))
        self.rfile.read(contentLength)
        body = b'{"accepted":true}'
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


@pytest.fixture()
def runningEchoServerBaseUrl():
    httpServer = ThreadingHTTPServer(("127.0.0.1", 0), _EchoHandler)
    serverThread = threading.Thread(target=httpServer.serve_forever, daemon=True)
    serverThread.start()
    host, port = httpServer.server_address
    yield f"http://{host}:{port}"
    httpServer.shutdown()
    httpServer.server_close()
    serverThread.join(timeout=5)


class TestMeasureRoundTripTimeSamplesOverHttp:
    def testCollectsRealTimedSamplesAgainstALiveServer(self, runningEchoServerBaseUrl):
        samples = measureRoundTripTimeSamplesOverHttp(f"{runningEchoServerBaseUrl}/health", sampleCount=5)
        assert len(samples) == 5
        assert all(sample > 0.0 for sample in samples)
        assert all(sample < 5000.0 for sample in samples)  # sane upper bound for a localhost round trip

    def testPostRequestWithBody(self, runningEchoServerBaseUrl):
        samples = measureRoundTripTimeSamplesOverHttp(
            f"{runningEchoServerBaseUrl}/orders/submit",
            sampleCount=3,
            requestBody=json.dumps({"symbol": "DEMO-EQ"}).encode("utf-8"),
            method="POST",
        )
        assert len(samples) == 3
        assert all(sample > 0.0 for sample in samples)

    def testRaisesOnUnreachableUrl(self):
        with pytest.raises(urllib.error.URLError):
            measureRoundTripTimeSamplesOverHttp("http://127.0.0.1:1/unreachable", sampleCount=1, timeoutSeconds=0.5)

    def testRaisesOnNonPositiveSampleCount(self):
        with pytest.raises(ValueError):
            measureRoundTripTimeSamplesOverHttp("http://127.0.0.1:1", sampleCount=0)


class TestCheckHttpEndpointIsReachable:
    def testReturnsTrueForALiveServer(self, runningEchoServerBaseUrl):
        assert checkHttpEndpointIsReachable(f"{runningEchoServerBaseUrl}/health") is True

    def testReturnsFalseForAnUnreachableUrl(self):
        assert checkHttpEndpointIsReachable("http://127.0.0.1:1/nope", timeoutSeconds=0.5) is False


class TestBuildLatencyHistogramAndPercentilesHandWorked:
    def testHandWorkedPercentilesOnTenSortedSamples(self):
        # samples 1..10 ms, already sorted. Nearest-rank convention:
        # index = floor(fraction * n). n=10.
        # p50: floor(0.5*10)=5 -> sortedSamples[5] = 6.0
        # p95: floor(0.95*10)=9 -> sortedSamples[9] = 10.0
        # p99: floor(0.99*10)=9 -> sortedSamples[9] = 10.0
        samples = [float(i) for i in range(1, 11)]
        result = buildLatencyHistogramAndPercentiles(samples, bucketCount=5)
        assert isinstance(result, LatencyBenchmarkResult)
        assert result.sampleCount == 10
        assert result.minimumMilliseconds == pytest.approx(1.0)
        assert result.maximumMilliseconds == pytest.approx(10.0)
        assert result.p50Milliseconds == pytest.approx(6.0)
        assert result.p95Milliseconds == pytest.approx(10.0)
        assert result.p99Milliseconds == pytest.approx(10.0)

    def testHistogramBucketCountsSumToTotalSamples(self):
        samples = [1.0, 2.0, 2.5, 5.0, 9.0, 9.5, 10.0]
        result = buildLatencyHistogramAndPercentiles(samples, bucketCount=3)
        assert len(result.histogramBuckets) == 3
        assert sum(bucket.sampleCount for bucket in result.histogramBuckets) == len(samples)

    def testHandWorkedHistogramBucketBoundaries(self):
        # span [0, 10], 5 buckets -> width 2.0 each.
        samples = [0.0, 1.0, 3.0, 5.0, 9.9, 10.0]
        result = buildLatencyHistogramAndPercentiles(samples, bucketCount=5)
        buckets = result.histogramBuckets
        assert buckets[0].lowerBoundInclusive == pytest.approx(0.0)
        assert buckets[0].upperBoundExclusive == pytest.approx(2.0)
        assert buckets[1].lowerBoundInclusive == pytest.approx(2.0)
        assert buckets[4].upperBoundExclusive == pytest.approx(10.0)
        # 0.0 and 1.0 fall in bucket 0; 3.0 in bucket 1; 5.0 in bucket 2;
        # 9.9 and 10.0 (clamped to last bucket) fall in bucket 4.
        assert buckets[0].sampleCount == 2
        assert buckets[1].sampleCount == 1
        assert buckets[2].sampleCount == 1
        assert buckets[4].sampleCount == 2

    def testAllIdenticalSamplesProduceOneBucket(self):
        result = buildLatencyHistogramAndPercentiles([5.0, 5.0, 5.0], bucketCount=10)
        assert len(result.histogramBuckets) == 1
        assert result.histogramBuckets[0].sampleCount == 3
        assert result.p50Milliseconds == pytest.approx(5.0)

    def testSingleSample(self):
        result = buildLatencyHistogramAndPercentiles([42.0])
        assert result.sampleCount == 1
        assert result.minimumMilliseconds == result.maximumMilliseconds == pytest.approx(42.0)
        assert result.p99Milliseconds == pytest.approx(42.0)

    def testRaisesOnEmptySamples(self):
        with pytest.raises(ValueError):
            buildLatencyHistogramAndPercentiles([])

    def testRaisesOnNonPositiveBucketCount(self):
        with pytest.raises(ValueError):
            buildLatencyHistogramAndPercentiles([1.0, 2.0], bucketCount=0)


class TestCompareLatencyAcrossVenues:
    def testSideBySideComparisonAcrossTwoVenues(self):
        results = compareLatencyAcrossVenues(
            {
                "VENUE-A": [1.0, 2.0, 3.0, 4.0, 5.0],
                "VENUE-B": [10.0, 20.0, 30.0, 40.0, 50.0],
            }
        )
        assert set(results.keys()) == {"VENUE-A", "VENUE-B"}
        assert results["VENUE-A"].maximumMilliseconds == pytest.approx(5.0)
        assert results["VENUE-B"].maximumMilliseconds == pytest.approx(50.0)
        # VENUE-B is uniformly 10x VENUE-A -> p50 should scale accordingly
        assert results["VENUE-B"].p50Milliseconds == pytest.approx(results["VENUE-A"].p50Milliseconds * 10)

    def testRaisesOnEmptyVenueDict(self):
        with pytest.raises(ValueError):
            compareLatencyAcrossVenues({})

    def testPropagatesPerVenueValueError(self):
        with pytest.raises(ValueError):
            compareLatencyAcrossVenues({"VENUE-A": []})
