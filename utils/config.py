import os
import json
import logging
from pathlib import Path
from threading import Lock
from utils.logger import logger

# Global configuration
BASE_DIR = Path(__file__).resolve().parent.parent
CONFIG = {}
config_lock = Lock()

def get_config(key, default=None):
    with config_lock:
        return CONFIG.get(key, default)

def save_config_value(key, value):
    """Update a single config value and save to disk"""
    global CONFIG
    try:
        config_path = BASE_DIR / 'config.json'
        
        # Read current file first to preserve comments/structure if possible (though json lib won't)
        # or just use current memory state? Better to read fresh to avoid overwriting external changes
        with open(config_path, 'r') as f:
            current_disk_config = json.load(f)
            
        current_disk_config[key] = value
        
        with open(config_path, 'w') as f:
            json.dump(current_disk_config, f, indent=2)
            
        # Update memory
        with config_lock:
            CONFIG[key] = value
            # Special handling for side effects
            if key == 'log_level':
                logger.setLevel(getattr(logging, value))
                
        logger.info(f"Config updated: {key} = {value}")
        return True
    except Exception as e:
        logger.error(f"Failed to save config: {e}")
        return False

def load_config():
    """Load configuration from JSON file"""
    global CONFIG
    try:
        config_path = BASE_DIR / 'config.json'
        with open(config_path, 'r') as f:
            new_config = json.load(f)
            
        with config_lock:
            CONFIG.clear()
            CONFIG.update(new_config)
            
            # Ensure storage path exists
            storage_path_str = CONFIG.get('storage_path', 'storage')
            if os.path.isabs(storage_path_str):
                storage_path = Path(storage_path_str)
            else:
                storage_path = BASE_DIR / storage_path_str
            
            storage_path.mkdir(parents=True, exist_ok=True)
            # Store resolved path back to config for easier access
            CONFIG['storage_path_resolved'] = str(storage_path)
            
            # Update log level if changed
            logger.setLevel(getattr(logging, CONFIG.get('log_level', 'INFO')))
            
        logger.info(f"Configuration loaded successfully from {config_path}")
        return True
    except Exception as e:
        logger.error(f"Failed to load config: {e}")
        return False
