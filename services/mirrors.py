import json
import socket
import requests
from threading import Lock
from utils.logger import logger
from services.database import db_lock, get_db_connection
from utils.config import get_config

# In-memory cache of DB mirrors
# Structure: {'name': {'urls': [...], 'status': 'approved'}}
MIRRORS_CACHE = {} 
mirrors_lock = Lock()

def load_mirrors_from_db():
    """Load mirrors from database"""
    global MIRRORS_CACHE
    try:
        with db_lock:
            conn = get_db_connection()
            cursor = conn.cursor()
            cursor.execute('SELECT name, urls, status FROM mirrors')
            rows = cursor.fetchall()
            
            with mirrors_lock:
                MIRRORS_CACHE.clear()
                for row in rows:
                    try:
                        MIRRORS_CACHE[row['name']] = {
                            'urls': json.loads(row['urls']),
                            'status': row['status']
                        }
                    except:
                        pass
            
            conn.close()
            logger.info(f"Loaded {len(MIRRORS_CACHE)} mirrors from database")
    except Exception as e:
        logger.error(f"Error loading mirrors from DB: {e}")

def is_self(host):
    """Check if the host refers to this server"""
    # Strip port
    hostname = host.split(':')[0]
    
    if hostname in ['localhost', '127.0.0.1', '::1', '0.0.0.0']:
        return True
        
    try:
        host_ip = socket.gethostbyname(hostname)
        
        # Get local IPs
        local_ips = set()
        local_hostname = socket.gethostname()
        try:
            local_ips.add(socket.gethostbyname(local_hostname))
        except:
            pass
            
        try:
            for info in socket.getaddrinfo(local_hostname, None):
                if info[4][0]:
                    local_ips.add(info[4][0])
        except:
            pass
            
        if host_ip in local_ips:
            return True
    except:
        pass
        
    return False

def validate_mirror(url):
    """Check if the mirror URL is reachable"""
    try:
        # Use HEAD request to check if it exists
        resp = requests.head(url, timeout=5, allow_redirects=True)
        return resp.status_code < 400
    except:
        return False

def save_mirror_to_db(name, urls, status='pending'):
    """Save a new dynamic mirror to the database"""
    
    # Validation: Check if self
    if is_self(name):
        logger.warning(f"Skipping self-referencing mirror: {name}")
        return False
        
    # Validation: Check reachability
    valid_urls = []
    for url in urls:
        if validate_mirror(url):
            valid_urls.append(url)
            
    if not valid_urls:
        logger.warning(f"No valid/reachable URLs for mirror: {name}")
        return False
        
    try:
        with db_lock:
            conn = get_db_connection()
            cursor = conn.cursor()
            cursor.execute('INSERT OR REPLACE INTO mirrors (name, urls, status) VALUES (?, ?, ?)', 
                          (name, json.dumps(valid_urls), status))
            conn.commit()
            conn.close()
            
        with mirrors_lock:
            MIRRORS_CACHE[name] = {
                'urls': valid_urls,
                'status': status
            }
            
        logger.info(f"Saved mirror: {name} -> {valid_urls} ({status})")
        return True
    except Exception as e:
        logger.error(f"Error saving mirror to DB: {e}")
        return False

def update_mirror(name, urls=None, status=None):
    """Update an existing mirror's URLs and/or status"""
    try:
        with mirrors_lock:
            if name not in MIRRORS_CACHE:
                return False
            current_data = MIRRORS_CACHE[name]
            
        new_urls = urls if urls is not None else current_data['urls']
        new_status = status if status is not None else current_data['status']
        
        if status is not None and status not in ['approved', 'pending', 'blacklisted']:
            return False

        # If URLs are being updated, validate them
        if urls is not None:
            valid_urls = []
            for url in urls:
                if validate_mirror(url):
                    valid_urls.append(url)
            if not valid_urls:
                logger.warning(f"No valid URLs provided for update: {name}")
                return False
            new_urls = valid_urls

        with db_lock:
            conn = get_db_connection()
            cursor = conn.cursor()
            cursor.execute('UPDATE mirrors SET urls = ?, status = ? WHERE name = ?', 
                          (json.dumps(new_urls), new_status, name))
            if cursor.rowcount == 0:
                return False
            conn.commit()
            conn.close()
            
        with mirrors_lock:
            MIRRORS_CACHE[name] = {
                'urls': new_urls,
                'status': new_status
            }
                
        logger.info(f"Updated mirror: {name} -> {new_urls} ({new_status})")
        return True
    except Exception as e:
        logger.error(f"Error updating mirror: {e}")
        return False

def delete_mirror(name):
    """Delete a mirror from the database"""
    try:
        with db_lock:
            conn = get_db_connection()
            cursor = conn.cursor()
            cursor.execute('DELETE FROM mirrors WHERE name = ?', (name,))
            conn.commit()
            conn.close()
            
        with mirrors_lock:
            if name in MIRRORS_CACHE:
                del MIRRORS_CACHE[name]
                
        logger.info(f"Deleted mirror: {name}")
        return True
    except Exception as e:
        logger.error(f"Error deleting mirror: {e}")
        return False

def get_all_mirrors():
    """Get only APPROVED mirrors for proxy usage"""
    # We no longer use config.json for mirrors, only DB
    approved_mirrors = {}
    with mirrors_lock:
        for name, data in MIRRORS_CACHE.items():
            if data['status'] == 'approved':
                approved_mirrors[name] = data['urls']
    return approved_mirrors

def get_mirrors_management():
    """Get ALL mirrors for management UI"""
    with mirrors_lock:
        return MIRRORS_CACHE.copy()

def get_upstream_key(distro, package_path):
    """Determine which upstream mirror to use based on path patterns"""
    path_lower = package_path.lower()
    mirrors = get_all_mirrors()
    
    # Check for security
    if 'security' in path_lower:
        sec_key = f"{distro}-security"
        if sec_key in mirrors:
            return sec_key
            
    return distro
