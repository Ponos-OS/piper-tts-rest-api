#!/usr/bin/env python3
"""
Load testing script for Piper TTS REST API.
Sends multiple concurrent requests to the /speak endpoint.
"""

import argparse
import os
import sys
import time
import random
import requests
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime
from typing import TypedDict, Optional

API_URL = "http://localhost:3000/v1/speak"
OUTPUT_DIR = "output"


class RequestResult(TypedDict):
    """Type definition for request result dictionary."""
    request_id: int
    success: bool
    message: str
    word_count: int
    char_count: int
    response_time: Optional[float]
    file_size: Optional[int]

# Word bank for generating random meaningful sentences
WORD_BANK = [
    "artificial", "intelligence", "machine", "learning", "technology", "computer", "system",
    "data", "algorithm", "network", "process", "software", "application", "development",
    "language", "natural", "processing", "synthesis", "speech", "voice", "audio", "sound",
    "digital", "innovation", "research", "science", "engineering", "programming", "code",
    "analysis", "pattern", "recognition", "model", "training", "neural", "deep", "automation",
    "interface", "interaction", "communication", "information", "knowledge", "capability",
    "feature", "function", "performance", "accuracy", "precision", "quality", "efficiency",
    "architecture", "framework", "platform", "infrastructure", "resource", "optimization",
    "implementation", "solution", "method", "approach", "technique", "strategy", "design",
    "structure", "component", "module", "service", "operation", "execution", "deployment",
    "integration", "configuration", "management", "monitoring", "evaluation", "testing",
    "validation", "verification", "security", "reliability", "scalability", "availability",
    "accessibility", "usability", "productivity", "collaboration", "workflow", "pipeline",
    "dataset", "database", "storage", "memory", "computation", "calculation", "processing",
    "generation", "transformation", "conversion", "encoding", "decoding", "compression",
    "extraction", "classification", "clustering", "prediction", "recommendation", "detection",
    "recognition", "understanding", "interpretation", "representation", "visualization",
    "advanced", "modern", "sophisticated", "complex", "robust", "powerful", "efficient",
    "effective", "innovative", "intelligent", "automated", "dynamic", "flexible", "adaptive",
    "scalable", "reliable", "secure", "fast", "accurate", "precise", "comprehensive",
    "integrated", "distributed", "parallel", "concurrent", "asynchronous", "real-time"
]

def generate_text(num_words: int) -> str:
    """
    Generate random meaningful text with the specified number of words.
    
    Args:
        num_words (int): Number of words to generate
        
    Returns:
        str: Generated text
    """
    if num_words < 1:
        return ""
    
    words = []
    for i in range(num_words):
        # Pick random word from word bank
        word = random.choice(WORD_BANK)
        
        # Capitalize first word of sentence
        if i == 0 or (i > 0 and words[-1].endswith(".")):
            word = word.capitalize()
        
        words.append(word)
        
        # Add punctuation randomly to create sentences
        if (i + 1) % random.randint(8, 15) == 0 and i < num_words - 1:
            words[-1] += "."
        elif random.random() < 0.1 and i < num_words - 1:
            words[-1] += ","
    
    # Ensure last word ends with period
    if not words[-1].endswith("."):
        words[-1] += "."
    
    return " ".join(words)


def send_request(request_id: int, num_words: int) -> RequestResult:
    """
    Send a single POST request to the /speak endpoint.
    
    Args:
        request_id (int): Unique identifier for this request
        num_words (int): Number of words to generate for the text
    
    Returns:
        RequestResult: Result containing request_id, success status, message, and performance metrics
    """
    try:
        text_content = generate_text(num_words)
        word_count = len(text_content.split())
        char_count = len(text_content)
        
        print(f"[Request {request_id}] Sending request (words: {word_count}, chars: {char_count})...")
        
        # Prepare the JSON payload
        payload = {"text": text_content}
        headers = {"Content-Type": "application/json"}
        
        # Send POST request and measure time
        start_time = time.time()
        response = requests.post(API_URL, json=payload, headers=headers, timeout=30)
        response_time = time.time() - start_time
        
        # Check if request was successful
        if response.status_code == 200:
            # Save the MP3 file
            output_file = os.path.join(OUTPUT_DIR, f"request_{request_id}.mp3")
            with open(output_file, "wb") as f:
                f.write(response.content)
            
            file_size = len(response.content)
            print(f"[Request {request_id}] ✓ Success - {response_time:.2f}s - {file_size} bytes - {output_file}")
            
            return RequestResult(
                request_id=request_id,
                success=True,
                message=f"Saved to {output_file} ({file_size} bytes)",
                file_size=file_size,
                response_time=response_time,
                word_count=word_count,
                char_count=char_count
            )
        else:
            error_msg = f"HTTP {response.status_code}: {response.text[:100]}"
            print(f"[Request {request_id}] ✗ Failed - {response_time:.2f}s - {error_msg}")
            
            return RequestResult(
                request_id=request_id,
                success=False,
                message=error_msg,
                response_time=response_time,
                word_count=word_count,
                char_count=char_count,
                file_size=None
            )
            
    except requests.exceptions.ConnectionError:
        error_msg = "Connection failed - Is the API running?"
        print(f"[Request {request_id}] ✗ Failed - {error_msg}")
        return RequestResult(
            request_id=request_id,
            success=False,
            message=error_msg,
            word_count=num_words,
            char_count=0,
            response_time=None,
            file_size=None
        )
    except requests.exceptions.Timeout:
        error_msg = "Request timed out"
        print(f"[Request {request_id}] ✗ Failed - {error_msg}")
        return RequestResult(
            request_id=request_id,
            success=False,
            message=error_msg,
            word_count=num_words,
            char_count=0,
            response_time=None,
            file_size=None
        )
    except Exception as e:
        error_msg = f"Unexpected error: {str(e)}"
        print(f"[Request {request_id}] ✗ Failed - {error_msg}")
        return RequestResult(
            request_id=request_id,
            success=False,
            message=error_msg,
            word_count=num_words,
            char_count=0,
            response_time=None,
            file_size=None
        )


def main():
    """Main function to orchestrate the load test."""
    parser = argparse.ArgumentParser(
        description="Load test the Piper TTS REST API with concurrent requests"
    )
    parser.add_argument(
        "-n", "--num-requests",
        type=int,
        default=5,
        help="Number of concurrent requests to send (default: 5)"
    )
    parser.add_argument(
        "-w", "--words",
        type=int,
        default=150,
        help="Number of words to generate for each request (default: 150)"
    )
    
    args = parser.parse_args()
    num_requests = args.num_requests
    num_words = args.words
    
    if num_requests < 1:
        print("Error: Number of requests must be at least 1")
        sys.exit(1)
    
    if num_words < 1:
        print("Error: Number of words must be at least 1")
        sys.exit(1)
    
    # Create output directory if it doesn't exist
    if not os.path.exists(OUTPUT_DIR):
        os.makedirs(OUTPUT_DIR)
        print(f"Created output directory: {OUTPUT_DIR}/")
    
    print(f"\n{'='*60}")
    print(f"Piper TTS API Load Test")
    print(f"{'='*60}")
    print(f"API Endpoint: {API_URL}")
    print(f"Concurrent Requests: {num_requests}")
    print(f"Words per Request: {num_words}")
    print(f"Output Directory: {OUTPUT_DIR}/")
    print(f"Start Time: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}")
    print(f"{'='*60}\n")
    
    # Execute concurrent requests
    results = []
    start_time = datetime.now()
    
    with ThreadPoolExecutor(max_workers=num_requests) as executor:
        # Submit all requests
        future_to_id = {
            executor.submit(send_request, i, num_words): i 
            for i in range(1, num_requests + 1)
        }
        
        # Collect results as they complete
        for future in as_completed(future_to_id):
            result = future.result()
            results.append(result)
    
    end_time = datetime.now()
    duration = (end_time - start_time).total_seconds()
    
    # Print summary
    print(f"\n{'='*60}")
    print(f"Load Test Summary")
    print(f"{'='*60}")
    
    successful = [r for r in results if r["success"]]
    failed = [r for r in results if not r["success"]]
    
    print(f"Total Requests: {num_requests}")
    print(f"Successful: {len(successful)}")
    print(f"Failed: {len(failed)}")
    print(f"Total Duration: {duration:.2f} seconds")
    
    if successful:
        total_bytes = sum(r.get("file_size", 0) for r in successful)
        print(f"Total Data Received: {total_bytes:,} bytes ({total_bytes/1024:.2f} KB)")
        
        print(f"\nIndividual Request Metrics:")
        print(f"{'ID':<6} {'Response Time':<15} {'Words':<8} {'Characters':<12} {'File Size (MB)':<15}")
        print(f"{'-'*6} {'-'*15} {'-'*8} {'-'*12} {'-'*15}")
        
        # Sort by request_id for consistent display
        for result in sorted(successful, key=lambda x: x["request_id"]):
            req_id = result["request_id"]
            resp_time = result.get("response_time", 0)
            words = result.get("word_count", 0)
            chars = result.get("char_count", 0)
            size_bytes = result.get("file_size", 0)
            size_mb = size_bytes / (1024 * 1024) if size_bytes else 0
            print(f"{req_id:<6} {resp_time:<15.2f} {words:<8} {chars:<12} {size_mb:<15.3f}")
    
    if failed:
        print(f"\nFailed Requests:")
        for result in failed:
            print(f"  - Request {result['request_id']}: {result['message']}")
    
    print(f"{'='*60}\n")
    
    # Exit with appropriate status code
    sys.exit(0 if len(failed) == 0 else 1)


if __name__ == "__main__":
    main()
