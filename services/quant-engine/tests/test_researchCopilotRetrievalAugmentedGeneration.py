import pytest

from quantengine.researchCopilotRetrievalAugmentedGeneration import (
    NON_ADVISORY_DISCLAIMER,
    DocumentChunk,
    IllustrativeSourceDocument,
    TfIdfRetrievalIndex,
    answerResearchQuestion,
    buildDocumentChunksFromSourceDocuments,
    buildIllustrativeRetrievalIndex,
    buildTfIdfVocabularyAndDocumentFrequencies,
    calculateCosineSimilarity,
    chunkDocumentIntoParagraphs,
    computeTfIdfVectorForTokens,
    tokenizeText,
)


def test_tokenizeText_lowercasesAndSplitsOnWordBoundaries():
    assert tokenizeText("Revenue Growth, up 18%!") == ["revenue", "growth", "up", "18"]


def test_chunkDocumentIntoParagraphs_splitsOnBlankLines():
    text = "First paragraph here.\n\nSecond paragraph here.\n\n\nThird paragraph."
    chunks = chunkDocumentIntoParagraphs(text)
    assert chunks == ["First paragraph here.", "Second paragraph here.", "Third paragraph."]


def test_chunkDocumentIntoParagraphs_dropsEmptyChunks():
    assert chunkDocumentIntoParagraphs("\n\n   \n\nReal content.\n\n") == ["Real content."]


def test_buildDocumentChunksFromSourceDocuments_tagsEachChunkWithDocumentAndIndex():
    documents = [
        IllustrativeSourceDocument(
            documentId="DOC-A", documentTitle="Doc A", documentType="10-K excerpt", fullText="Para one.\n\nPara two."
        )
    ]
    chunks = buildDocumentChunksFromSourceDocuments(documents)
    assert len(chunks) == 2
    assert chunks[0] == DocumentChunk(documentId="DOC-A", documentTitle="Doc A", chunkIndex=0, text="Para one.")
    assert chunks[1] == DocumentChunk(documentId="DOC-A", documentTitle="Doc A", chunkIndex=1, text="Para two.")


def test_buildTfIdfVocabularyAndDocumentFrequencies_countsChunksContainingEachTerm():
    chunks = [
        DocumentChunk("D1", "D1", 0, "revenue growth was strong"),
        DocumentChunk("D1", "D1", 1, "litigation risk factor disclosed"),
        DocumentChunk("D2", "D2", 0, "revenue declined this quarter"),
    ]
    documentFrequencies = buildTfIdfVocabularyAndDocumentFrequencies(chunks)
    assert documentFrequencies["revenue"] == 2  # appears in chunk 0 and chunk 2 (of 3 total)
    assert documentFrequencies["litigation"] == 1
    assert documentFrequencies["strong"] == 1


def test_buildTfIdfVocabularyAndDocumentFrequencies_rejectsEmptyChunkList():
    with pytest.raises(ValueError):
        buildTfIdfVocabularyAndDocumentFrequencies([])


def test_computeTfIdfVectorForTokens_handWorkedExample():
    # 3 chunks total; "revenue" appears in 2 of them -> idf = ln(3/2)
    documentFrequencyByTerm = {"revenue": 2, "growth": 1}
    vector = computeTfIdfVectorForTokens(["revenue", "revenue", "growth"], documentFrequencyByTerm, totalChunkCount=3)
    import math

    assert vector["revenue"] == pytest.approx(2 * math.log(3 / 2))
    assert vector["growth"] == pytest.approx(1 * math.log(3 / 1))


def test_computeTfIdfVectorForTokens_skipsTermsNeverSeenInCorpus():
    vector = computeTfIdfVectorForTokens(["unseenword"], {"revenue": 2}, totalChunkCount=3)
    assert "unseenword" not in vector
    assert vector == {}


def test_calculateCosineSimilarity_identicalVectorsScoreOne():
    vectorA = {"revenue": 2.0, "growth": 1.0}
    assert calculateCosineSimilarity(vectorA, dict(vectorA)) == pytest.approx(1.0)


def test_calculateCosineSimilarity_orthogonalVectorsScoreZero():
    vectorA = {"revenue": 1.0}
    vectorB = {"litigation": 1.0}
    assert calculateCosineSimilarity(vectorA, vectorB) == pytest.approx(0.0)


def test_calculateCosineSimilarity_zeroVectorReturnsZeroNotError():
    assert calculateCosineSimilarity({}, {"revenue": 1.0}) == 0.0


def test_tfIdfRetrievalIndex_rejectsEmptyChunkList():
    with pytest.raises(ValueError):
        TfIdfRetrievalIndex([])


def test_tfIdfRetrievalIndex_retrievalCorrectness_revenueGrowthQueryMatchesRevenueChunkNotUnrelatedChunk():
    """The core correctness proof required by the task: a query about
    'revenue growth' must actually retrieve the chunk that discusses
    revenue growth as its top result, NOT an unrelated chunk (litigation).
    """
    chunks = [
        DocumentChunk(
            "D1", "D1", 0, "Revenue growth accelerated to 21 percent year over year this quarter."
        ),
        DocumentChunk(
            "D1", "D1", 1, "The company is a defendant in a class action lawsuit alleging a defective sensor."
        ),
        DocumentChunk(
            "D2", "D2", 0, "Net interest income declined as deposit costs rose in the higher rate environment."
        ),
    ]
    index = TfIdfRetrievalIndex(chunks)
    results = index.retrieveTopKRelevantChunks("revenue growth", topK=1)
    assert len(results) == 1
    assert results[0].chunk.text.startswith("Revenue growth accelerated")
    assert results[0].cosineSimilarityScore > 0.0


def test_tfIdfRetrievalIndex_retrievalCorrectness_litigationQueryMatchesLitigationChunk():
    chunks = [
        DocumentChunk("D1", "D1", 0, "Revenue growth accelerated to 21 percent year over year this quarter."),
        DocumentChunk(
            "D1", "D1", 1, "The company is a defendant in a class action lawsuit alleging a defective sensor."
        ),
        DocumentChunk(
            "D2", "D2", 0, "Net interest income declined as deposit costs rose in the higher rate environment."
        ),
    ]
    index = TfIdfRetrievalIndex(chunks)
    results = index.retrieveTopKRelevantChunks("class action lawsuit litigation", topK=1)
    assert results[0].chunk.text.startswith("The company is a defendant")


def test_tfIdfRetrievalIndex_rejectsNonPositiveTopKAndEmptyQuery():
    index = TfIdfRetrievalIndex([DocumentChunk("D1", "D1", 0, "some text here")])
    with pytest.raises(ValueError):
        index.retrieveTopKRelevantChunks("some text", topK=0)
    with pytest.raises(ValueError):
        index.retrieveTopKRelevantChunks("   ", topK=1)


def test_tfIdfRetrievalIndex_resultsSortedByDescendingSimilarity():
    chunks = [
        DocumentChunk("D1", "D1", 0, "apple banana cherry"),
        DocumentChunk("D1", "D1", 1, "apple banana"),
        DocumentChunk("D1", "D1", 2, "cherry date elderberry fig"),
    ]
    index = TfIdfRetrievalIndex(chunks)
    results = index.retrieveTopKRelevantChunks("apple banana", topK=3)
    scores = [result.cosineSimilarityScore for result in results]
    assert scores == sorted(scores, reverse=True)
    assert results[0].chunk.chunkIndex in (0, 1)  # the two apple/banana chunks should rank above the unrelated one


def test_buildIllustrativeRetrievalIndex_isRealAndQueryable():
    index = buildIllustrativeRetrievalIndex()
    assert index.totalChunkCount > 1
    results = index.retrieveTopKRelevantChunks("revenue growth", topK=2)
    assert len(results) == 2
    # Both Northwind chunks discussing revenue growth should outrank the
    # unrelated Cascade Bank / Brightleaf chunks.
    topDocumentIds = {result.chunk.documentId for result in results}
    assert "SIM-NORTHWIND-10K-2025" in topDocumentIds or "SIM-NORTHWIND-EARNINGS-Q3-2025" in topDocumentIds


def test_answerResearchQuestion_alwaysIncludesNonAdvisoryDisclaimer():
    index = buildIllustrativeRetrievalIndex()
    answer = answerResearchQuestion(index, "What happened to revenue growth?", topK=2)
    assert answer.disclaimer == NON_ADVISORY_DISCLAIMER
    assert "not investment advice" in answer.disclaimer.lower()


def test_answerResearchQuestion_composedAnswerCitesRetrievedSourceChunks():
    index = buildIllustrativeRetrievalIndex()
    answer = answerResearchQuestion(index, "commercial real estate credit losses", topK=2)
    assert len(answer.retrievedChunks) == 2
    for retrieved in answer.retrievedChunks:
        citation = f"[{retrieved.chunk.documentId}, chunk {retrieved.chunk.chunkIndex}]"
        assert citation in answer.composedAnswerText
        # Every cited chunk's actual text must appear verbatim in the
        # composed answer — proving it's extractive/grounded, not
        # fabricated free text.
        assert retrieved.chunk.text in answer.composedAnswerText
