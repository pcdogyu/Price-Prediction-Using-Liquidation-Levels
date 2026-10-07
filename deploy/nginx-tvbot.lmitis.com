server {
    server_name tvbot.lmitis.com;

    access_log /var/log/nginx/tv-okx-bot.access.log;
    error_log /var/log/nginx/tv-okx-bot.error.log;

    include /etc/nginx/snippets/liquidation-locations.conf;

    location / {
        proxy_pass http://127.0.0.1:18080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Connection "";
        proxy_read_timeout 60s;
        proxy_send_timeout 60s;
    }

    listen [::]:443 ssl ipv6only=on; # managed by Certbot
    listen 443 ssl; # managed by Certbot
    ssl_certificate /etc/letsencrypt/live/tvbot.lmitis.com/fullchain.pem; # managed by Certbot
    ssl_certificate_key /etc/letsencrypt/live/tvbot.lmitis.com/privkey.pem; # managed by Certbot
    include /etc/letsencrypt/options-ssl-nginx.conf; # managed by Certbot
    ssl_dhparam /etc/letsencrypt/ssl-dhparams.pem; # managed by Certbot
}

server {
    if ($host = tvbot.lmitis.com) {
        return 301 https://$host$request_uri;
    } # managed by Certbot

    listen 80;
    listen [::]:80;
    server_name tvbot.lmitis.com;
    return 404; # managed by Certbot
}
