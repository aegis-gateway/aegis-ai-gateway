"""PII scanner using Microsoft Presidio with spaCy NER."""

from dataclasses import dataclass

from presidio_analyzer import AnalyzerEngine
from presidio_anonymizer import AnonymizerEngine


@dataclass
class PIIDetection:
    entity_type: str
    start: int
    end: int
    score: float


@dataclass
class ScanResult:
    detected: bool
    detections: list[PIIDetection]
    redacted_text: str


class PIIScanner:
    """Scans text for PII using Presidio analyzer + spaCy en_core_web_lg."""

    def __init__(self):
        self.analyzer = AnalyzerEngine()
        self.anonymizer = AnonymizerEngine()

    def scan(self, text: str, classification: str = "INTERNAL") -> ScanResult:
        """Scan text for PII entities.

        Args:
            text: The text to scan.
            classification: Data classification level.

        Returns:
            ScanResult with detections and optionally redacted text.
        """
        results = self.analyzer.analyze(
            text=text,
            language="en",
            score_threshold=0.7,
        )

        if not results:
            return ScanResult(detected=False, detections=[], redacted_text=text)

        detections = [
            PIIDetection(
                entity_type=r.entity_type,
                start=r.start,
                end=r.end,
                score=r.score,
            )
            for r in results
        ]

        # Redact if classification warrants it
        redacted_text = text
        if classification in ("INTERNAL", "CONFIDENTIAL", "RESTRICTED"):
            anonymized = self.anonymizer.anonymize(text=text, analyzer_results=results)
            redacted_text = anonymized.text

        return ScanResult(
            detected=True,
            detections=detections,
            redacted_text=redacted_text,
        )
