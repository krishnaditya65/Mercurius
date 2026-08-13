use std::collections::HashMap;

use crate::marketDataEventTypes::{PriceLevelDeltaUpdate, SequencedMarketDataMessage};

/// Assigns per-instrument monotonic sequence numbers to outgoing delta
/// messages and hands them to every registered downstream sink.
///
/// TODO(real build): the real publisher consumes the matching engine's
/// outbound ring buffer (ARCHITECTURE.md §5) and writes to Kafka/Redpanda,
/// partitioned by instrument, as the durable backbone every downstream
/// consumer reads from independently. This skeleton simulates that with an
/// in-process `Vec<Box<dyn Fn>>` fan-out instead — good enough to prove the
/// sequencing/delta-compression contract, not a substitute for the real
/// Kafka producer + WS fan-out fleet.
pub struct DeltaPublisher {
    lastSequenceNumberByInstrument: HashMap<String, u64>,
    registeredDownstreamSinks: Vec<Box<dyn FnMut(&SequencedMarketDataMessage)>>,
}

impl DeltaPublisher {
    pub fn newPublisherWithNoSinks() -> Self {
        DeltaPublisher {
            lastSequenceNumberByInstrument: HashMap::new(),
            registeredDownstreamSinks: Vec::new(),
        }
    }

    /// Registers a downstream sink (in the real build: a Kafka producer
    /// write, or a WS fan-out worker's local queue). Every published
    /// message is delivered to every registered sink.
    pub fn registerDownstreamSink<SinkFn>(&mut self, downstreamSink: SinkFn)
    where
        SinkFn: FnMut(&SequencedMarketDataMessage) + 'static,
    {
        self.registeredDownstreamSinks.push(Box::new(downstreamSink));
    }

    /// Publishes one batch of price-level deltas for a single instrument,
    /// assigning the next per-instrument sequence number and fanning the
    /// resulting message out to every registered sink.
    pub fn publishDeltaBatchForInstrument(
        &mut self,
        instrumentSymbol: &str,
        deltaUpdatesInThisBatch: Vec<PriceLevelDeltaUpdate>,
    ) {
        let nextSequenceNumberForInstrument = self
            .lastSequenceNumberByInstrument
            .entry(instrumentSymbol.to_string())
            .and_modify(|sequenceNumber| *sequenceNumber += 1)
            .or_insert(1);

        let sequencedMessage = SequencedMarketDataMessage {
            instrumentSymbol: instrumentSymbol.to_string(),
            perInstrumentSequenceNumber: *nextSequenceNumberForInstrument,
            deltaUpdatesInThisMessage: deltaUpdatesInThisBatch,
        };

        for downstreamSink in self.registeredDownstreamSinks.iter_mut() {
            downstreamSink(&sequencedMessage);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::RefCell;
    use std::rc::Rc;

    #[test]
    fn sequenceNumberIncrementsIndependentlyPerInstrument() {
        let receivedMessages = Rc::new(RefCell::new(Vec::new()));
        let receivedMessagesForSink = Rc::clone(&receivedMessages);

        let mut deltaPublisherUnderTest = DeltaPublisher::newPublisherWithNoSinks();
        deltaPublisherUnderTest.registerDownstreamSink(move |message| {
            receivedMessagesForSink.borrow_mut().push(message.clone_for_test());
        });

        deltaPublisherUnderTest.publishDeltaBatchForInstrument("AAPL", vec![]);
        deltaPublisherUnderTest.publishDeltaBatchForInstrument("MSFT", vec![]);
        deltaPublisherUnderTest.publishDeltaBatchForInstrument("AAPL", vec![]);

        let recorded = receivedMessages.borrow();
        assert_eq!(recorded[0].perInstrumentSequenceNumber, 1); // AAPL #1
        assert_eq!(recorded[1].perInstrumentSequenceNumber, 1); // MSFT #1, independent counter
        assert_eq!(recorded[2].perInstrumentSequenceNumber, 2); // AAPL #2
    }

    // Test-only helper since SequencedMarketDataMessage doesn't derive Clone
    // in the skeleton (real build should reconsider that once the wire
    // format is finalized).
    impl SequencedMarketDataMessage {
        fn clone_for_test(&self) -> Self {
            SequencedMarketDataMessage {
                instrumentSymbol: self.instrumentSymbol.clone(),
                perInstrumentSequenceNumber: self.perInstrumentSequenceNumber,
                deltaUpdatesInThisMessage: self.deltaUpdatesInThisMessage.clone(),
            }
        }
    }
}
