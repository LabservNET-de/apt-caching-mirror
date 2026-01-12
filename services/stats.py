import json
import os
import time
from datetime import datetime
from threading import Lock
from utils.logger import logger
from services.database import db_lock, get_db_connection
from utils.config import get_config
from pathlib import Path

STATS = {
    'requests_total': 0,
    'cache_hits': 0,
    'cache_misses': 0,
    'bytes_served': 0,
    'start_time': datetime.now()
}
FILE_STATS = {
    'total_files': 0,
    'total_size': 0,
    'distro_stats': {}
}
LOG_BUFFER = []
MAX_LOG_BUFFER = 100

stats_lock = Lock()
file_stats_lock = Lock()
log_lock = Lock()

def add_log(message, level='INFO'):
    """Add a log message to the in-memory buffer"""
    timestamp = datetime.now().strftime('%H:%M:%S')
    entry = {'time': timestamp, 'level': level, 'message': message}
    
    with log_lock:
        LOG_BUFFER.append(entry)
        if len(LOG_BUFFER) > MAX_LOG_BUFFER:
            LOG_BUFFER.pop(0)

def load_stats_from_db():
    """Load statistics from database into memory"""
    try:
        with db_lock:
            conn = get_db_connection()
            cursor = conn.cursor()
            cursor.execute('SELECT key, value FROM stats')
            rows = cursor.fetchall()
            
            with stats_lock:
                for row in rows:
                    if row['key'] in STATS:
                        STATS[row['key']] = row['value']
            
            conn.close()
            logger.info("Stats loaded from database")
    except Exception as e:
        logger.error(f"Error loading stats from DB: {e}")

def save_stats_to_db():
    """Save current in-memory stats to database"""
    try:
        with db_lock:
            conn = get_db_connection()
            cursor = conn.cursor()
            
            with stats_lock:
                for key in ['requests_total', 'cache_hits', 'cache_misses', 'bytes_served']:
                    cursor.execute('UPDATE stats SET value = ? WHERE key = ?', (STATS[key], key))
            
            conn.commit()
            conn.close()
    except Exception as e:
        logger.error(f"Error saving stats to DB: {e}")

def update_file_stats():
    """Calculate file statistics (expensive operation)"""
    total_files = 0
    total_size = 0
    distro_stats = {}
    storage_path_str = get_config('storage_path_resolved')
    
    if storage_path_str:
        storage_path = Path(storage_path_str)
        if storage_path.exists():
            for root, dirs, files in os.walk(storage_path):
                rel_path = os.path.relpath(root, storage_path)
                distro = rel_path.split(os.sep)[0]
                
                # Skip if we are at root or hidden folders
                if distro == '.' or distro.startswith('.'):
                    continue
                    
                if distro not in distro_stats:
                    distro_stats[distro] = {'files': 0, 'size': 0}

                distro_stats[distro]['files'] += len(files)
                total_files += len(files)
                
                for filename in files:
                    filepath = os.path.join(root, filename)
                    try:
                        fsize = os.path.getsize(filepath)
                        total_size += fsize
                        distro_stats[distro]['size'] += fsize
                    except:
                        pass
    
    with file_stats_lock:
        FILE_STATS['total_files'] = total_files
        FILE_STATS['total_size'] = total_size
        FILE_STATS['distro_stats'] = distro_stats
    
    logger.info("File stats updated")
